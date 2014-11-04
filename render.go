// Copyright 2013 Martini Authors
// Copyright 2014 Unknwon
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package macaron

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Unknwon/macaron/bpool"
)

const (
	ContentType    = "Content-Type"
	ContentLength  = "Content-Length"
	ContentBinary  = "application/octet-stream"
	ContentJSON    = "application/json"
	ContentHTML    = "text/html"
	ContentXHTML   = "application/xhtml+xml"
	ContentXML     = "text/xml"
	defaultCharset = "UTF-8"
)

var (
	// Provides a temporary buffer to execute templates into and catch errors.
	bufpool = bpool.NewBufferPool(64)

	// Included helper functions for use when rendering html
	helperFuncs = template.FuncMap{
		"yield": func() (string, error) {
			return "", fmt.Errorf("yield called with no layout defined")
		},
		"current": func() (string, error) {
			return "", nil
		},
	}
)

type (
	// Delims represents a set of Left and Right delimiters for HTML template rendering
	Delims struct {
		// Left delimiter, defaults to {{
		Left string
		// Right delimiter, defaults to }}
		Right string
	}

	// RenderOptions represents a struct for specifying configuration options for the Render middleware.
	RenderOptions struct {
		// Name of template set, leave empty to be default.
		Name string
		// Directory to load templates. Default is "templates".
		Directory string
		// Layout template name. Will not render a layout if "". Default is to "".
		Layout string
		// Extensions to parse template files from. Defaults are [".tmpl", ".html"].
		Extensions []string
		// Funcs is a slice of FuncMaps to apply to the template upon compilation. This is useful for helper functions. Default is [].
		Funcs []template.FuncMap
		// Delims sets the action delimiters to the specified strings in the Delims struct.
		Delims Delims
		// Appends the given charset to the Content-Type header. Default is "UTF-8".
		Charset string
		// Outputs human readable JSON.
		IndentJSON bool
		// Outputs human readable XML.
		IndentXML bool
		// Prefixes the JSON output with the given bytes.
		PrefixJSON []byte
		// Prefixes the XML output with the given bytes.
		PrefixXML []byte
		// Allows changing of output to XHTML instead of HTML. Default is "text/html"
		HTMLContentType string
	}

	// HTMLOptions is a struct for overriding some rendering Options for specific HTML call
	HTMLOptions struct {
		// Layout template name. Overrides Options.Layout.
		Layout string
	}
)

type Render interface {
	http.ResponseWriter
	RW() http.ResponseWriter

	JSON(int, interface{})
	JSONString(interface{}) (string, error)
	RawData(int, []byte)
	RenderData(int, []byte)
	HTML(int, string, interface{}, ...HTMLOptions)
	HTMLSet(int, string, string, interface{}, ...HTMLOptions)
	HTMLString(string, interface{}, ...HTMLOptions) (string, error)
	HTMLSetString(string, string, interface{}, ...HTMLOptions) (string, error)
	XML(int, interface{})
	Error(int, ...string)
	Status(int)
	Redirect(string, ...int)
	SetTemplatePath(string, string)
}

const (
	_DEFAULT_TPL_SET_NAME = "DEFAULT"
)

var (
	tplSets    = make(map[string]*template.Template)
	tplSetOpts = make(map[string]*RenderOptions)
	lock       sync.RWMutex
)

func prepareOptions(options []RenderOptions) *RenderOptions {
	var opt RenderOptions
	if len(options) > 0 {
		opt = options[0]
	}

	// Defaults.
	if len(opt.Name) == 0 {
		opt.Name = _DEFAULT_TPL_SET_NAME
	}
	if len(opt.Directory) == 0 {
		opt.Directory = "templates"
	}
	if len(opt.Extensions) == 0 {
		opt.Extensions = []string{".tmpl", ".html"}
	}
	if len(opt.HTMLContentType) == 0 {
		opt.HTMLContentType = ContentHTML
	}

	lock.RLock()
	defer lock.RUnlock()

	tplSetOpts[opt.Name] = &opt
	return &opt
}

func prepareCharset(charset string) string {
	if len(charset) != 0 {
		return "; charset=" + charset
	}

	return "; charset=" + defaultCharset
}

func getExt(s string) string {
	if strings.Index(s, ".") == -1 {
		return ""
	}
	return "." + strings.Join(strings.Split(s, ".")[1:], ".")
}

func compile(options *RenderOptions) {
	dir := options.Directory
	t := template.New(dir)
	t.Delims(options.Delims.Left, options.Delims.Right)
	// Parse an initial template in case we don't have any.
	template.Must(t.Parse("Macaron"))

	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		r, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		ext := getExt(r)

		for _, extension := range options.Extensions {
			if ext == extension {

				buf, err := ioutil.ReadFile(path)
				if err != nil {
					panic(err)
				}

				name := (r[0 : len(r)-len(ext)])
				tmpl := t.New(filepath.ToSlash(name))

				// add our funcmaps
				for _, funcs := range options.Funcs {
					tmpl.Funcs(funcs)
				}

				// Bomb out if parse fails. We don't want any silent server starts.
				template.Must(tmpl.Funcs(helperFuncs).Parse(string(buf)))
				break
			}
		}

		return nil
	}); err != nil {
		panic("fail to walk templates directory: " + err.Error())
	}

	lock.Lock()
	defer lock.Unlock()

	tplSets[options.Name] = t
}

// Renderer is a Middleware that maps a macaron.Render service into the Macaron handler chain.
// An single variadic macaron.RenderOptions struct can be optionally provided to configure
// HTML rendering. The default directory for templates is "templates" and the default
// file extension is ".tmpl" and ".html".
//
// If MACARON_ENV is set to "" or "development" then templates will be recompiled on every request. For more performance, set the
// MACARON_ENV environment variable to "production".
func Renderer(options ...RenderOptions) Handler {
	opt := prepareOptions(options)
	cs := prepareCharset(opt.Charset)
	compile(opt)

	return func(ctx *Context, rw http.ResponseWriter, req *http.Request) {
		r := &TplRender{
			ResponseWriter:  rw,
			Req:             req,
			Opt:             opt,
			CompiledCharset: cs,
		}
		ctx.Data["TmplLoadTimes"] = func() string {
			if r.startTime.IsZero() {
				return ""
			}
			return fmt.Sprint(time.Since(r.startTime).Nanoseconds()/1e6) + "ms"
		}

		ctx.Render = r
		ctx.MapTo(r, (*Render)(nil))
	}
}

type TplRender struct {
	http.ResponseWriter
	Req             *http.Request
	Opt             *RenderOptions
	CompiledCharset string

	startTime time.Time
}

func (r *TplRender) RW() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *TplRender) JSON(status int, v interface{}) {
	var result []byte
	var err error
	if r.Opt.IndentJSON {
		result, err = json.MarshalIndent(v, "", "  ")
	} else {
		result, err = json.Marshal(v)
	}
	if err != nil {
		http.Error(r, err.Error(), 500)
		return
	}

	// json rendered fine, write out the result
	r.Header().Set(ContentType, ContentJSON+r.CompiledCharset)
	r.WriteHeader(status)
	if len(r.Opt.PrefixJSON) > 0 {
		r.Write(r.Opt.PrefixJSON)
	}
	r.Write(result)
}

func (r *TplRender) JSONString(v interface{}) (string, error) {
	var result []byte
	var err error
	if r.Opt.IndentJSON {
		result, err = json.MarshalIndent(v, "", "  ")
	} else {
		result, err = json.Marshal(v)
	}
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (r *TplRender) XML(status int, v interface{}) {
	var result []byte
	var err error
	if r.Opt.IndentXML {
		result, err = xml.MarshalIndent(v, "", "  ")
	} else {
		result, err = xml.Marshal(v)
	}
	if err != nil {
		http.Error(r, err.Error(), 500)
		return
	}

	// XML rendered fine, write out the result
	r.Header().Set(ContentType, ContentXML+r.CompiledCharset)
	r.WriteHeader(status)
	if len(r.Opt.PrefixXML) > 0 {
		r.Write(r.Opt.PrefixXML)
	}
	r.Write(result)
}

func (r *TplRender) data(status int, contentType string, v []byte) {
	if r.Header().Get(ContentType) == "" {
		r.Header().Set(ContentType, contentType)
	}
	r.WriteHeader(status)
	r.Write(v)
}

func (r *TplRender) RawData(status int, v []byte) {
	r.data(status, ContentBinary, v)
}

func (r *TplRender) RenderData(status int, v []byte) {
	r.data(status, ContentHTML, v)
}

func (r *TplRender) execute(t *template.Template, name string, data interface{}) (*bytes.Buffer, error) {
	buf := bufpool.Get()
	return buf, t.ExecuteTemplate(buf, name, data)
}

func (r *TplRender) addYield(t *template.Template, tplName string, data interface{}) {
	funcs := template.FuncMap{
		"yield": func() (template.HTML, error) {
			buf, err := r.execute(t, tplName, data)
			// return safe html here since we are rendering our own template
			return template.HTML(buf.String()), err
		},
		"current": func() (string, error) {
			return tplName, nil
		},
	}
	t.Funcs(funcs)
}

func (r *TplRender) renderBytes(setName, tplName string, data interface{}, htmlOpt ...HTMLOptions) (*bytes.Buffer, error) {
	renderOpt := tplSetOpts[setName]
	if Env == DEV {
		compile(renderOpt)
	}

	lock.RLock()
	defer lock.RUnlock()

	t := tplSets[setName]
	if t == nil {
		return nil, fmt.Errorf("html/template: template \"%s\" is undefined", tplName)
	}

	opt := r.prepareHTMLOptions(htmlOpt)

	if len(opt.Layout) > 0 {
		r.addYield(t, tplName, data)
		tplName = opt.Layout
	}

	out, err := r.execute(t, tplName, data)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func (r *TplRender) renderHTML(status int, setName, tplName string, data interface{}, htmlOpt ...HTMLOptions) {
	r.startTime = time.Now()

	out, err := r.renderBytes(setName, tplName, data, htmlOpt...)
	if err != nil {
		http.Error(r, err.Error(), http.StatusInternalServerError)
		return
	}

	r.Header().Set(ContentType, r.Opt.HTMLContentType+r.CompiledCharset)
	r.WriteHeader(status)

	io.Copy(r, out)
	bufpool.Put(out)
}

func (r *TplRender) HTML(status int, name string, data interface{}, htmlOpt ...HTMLOptions) {
	r.renderHTML(status, _DEFAULT_TPL_SET_NAME, name, data, htmlOpt...)
}

func (r *TplRender) HTMLSet(status int, setName, tplName string, data interface{}, htmlOpt ...HTMLOptions) {
	r.renderHTML(status, setName, tplName, data, htmlOpt...)
}

func (r *TplRender) renderHTMLString(setName, tplName string, data interface{}, htmlOpt ...HTMLOptions) (string, error) {
	if out, err := r.renderBytes(setName, tplName, data, htmlOpt...); err != nil {
		return "", err
	} else {
		return out.String(), nil
	}
}

func (r *TplRender) HTMLString(name string, data interface{}, htmlOpt ...HTMLOptions) (string, error) {
	return r.renderHTMLString(_DEFAULT_TPL_SET_NAME, name, data, htmlOpt...)
}

func (r *TplRender) HTMLSetString(setName, tplName string, data interface{}, htmlOpt ...HTMLOptions) (string, error) {
	return r.renderHTMLString(setName, tplName, data, htmlOpt...)
}

// Error writes the given HTTP status to the current ResponseWriter
func (r *TplRender) Error(status int, message ...string) {
	r.WriteHeader(status)
	if len(message) > 0 {
		r.Write([]byte(message[0]))
	}
}

func (r *TplRender) Status(status int) {
	r.WriteHeader(status)
}

func (r *TplRender) Redirect(location string, status ...int) {
	code := http.StatusFound
	if len(status) == 1 {
		code = status[0]
	}

	http.Redirect(r, r.Req, location, code)
}

func (r *TplRender) prepareHTMLOptions(htmlOpt []HTMLOptions) HTMLOptions {
	if len(htmlOpt) > 0 {
		return htmlOpt[0]
	}

	return HTMLOptions{
		Layout: r.Opt.Layout,
	}
}

func (r *TplRender) SetTemplatePath(setName, dir string) {
	if len(setName) == 0 {
		setName = _DEFAULT_TPL_SET_NAME
	}
	opt := tplSetOpts[setName]
	opt.Directory = dir
	compile(opt)
}
