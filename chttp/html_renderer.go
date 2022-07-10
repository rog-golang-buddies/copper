package chttp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/gocopper/copper/clogger"
	"github.com/gocopper/copper/crandom"

	"github.com/gocopper/copper/cerrors"
)

var (
	//go:embed livewire_styles.html
	livewireStylesHTML template.HTML

	//go:embed livewire_script.html
	livewireScriptHTML template.HTML
)

type (
	// HTMLDir is a directory that can be embedded or found on the host system. It should contain sub-directories
	// and files to support the WriteHTML function in ReaderWriter.
	HTMLDir fs.FS

	// StaticDir represents a directory that holds static resources (JS, CSS, images, etc.)
	StaticDir fs.FS

	// HTMLRenderer provides functionality in rendering templatized HTML along with HTML components
	HTMLRenderer struct {
		htmlDir                 HTMLDir
		staticDir               StaticDir
		renderFuncs             []HTMLRenderFunc
		livewireComponentByName map[string]LivewireComponent
	}

	// HTMLRenderFunc can be used to register new template functions
	HTMLRenderFunc struct {
		// Name for the function that can be invoked in a template
		Name string

		// Func should return a function that takes in any number of params and returns either a single return value,
		// or two return values of which the second has type error.
		Func func(r *http.Request) interface{}
	}

	// NewHTMLRendererParams holds the params needed to create HTMLRenderer
	NewHTMLRendererParams struct {
		HTMLDir            HTMLDir
		StaticDir          StaticDir
		RenderFuncs        []HTMLRenderFunc
		LivewireComponents []LivewireComponent
		Config             Config
		Logger             clogger.Logger
	}
)

// NewHTMLRenderer creates a new HTMLRenderer with HTML templates stored in dir and registers the provided HTML
// components
func NewHTMLRenderer(p NewHTMLRendererParams) (*HTMLRenderer, error) {
	hr := HTMLRenderer{
		htmlDir:                 p.HTMLDir,
		staticDir:               p.StaticDir,
		renderFuncs:             p.RenderFuncs,
		livewireComponentByName: make(map[string]LivewireComponent, len(p.LivewireComponents)),
	}

	for i := range p.LivewireComponents {
		hr.livewireComponentByName[p.LivewireComponents[i].Name()] = p.LivewireComponents[i]
	}

	if p.Config.UseLocalHTML {
		wd, err := os.Getwd()
		if err != nil {
			return nil, cerrors.New(err, "failed to get current working directory", nil)
		}

		hr.htmlDir = os.DirFS(filepath.Join(wd, "web"))
	}

	return &hr, nil
}

func (r *HTMLRenderer) funcMap(req *http.Request) template.FuncMap {
	var funcMap = template.FuncMap{
		"partial":        r.partial(req),
		"livewire":       r.livewireInitial(req),
		"livewireStyles": func() template.HTML { return livewireStylesHTML },
		"livewireScript": func() template.HTML { return livewireScriptHTML },
	}

	for i := range r.renderFuncs {
		funcMap[r.renderFuncs[i].Name] = r.renderFuncs[i].Func(req)
	}

	return funcMap
}

func (r *HTMLRenderer) render(req *http.Request, layout, page string, data interface{}) (template.HTML, error) {
	var dest strings.Builder

	tmpl, err := template.New(layout).
		Funcs(r.funcMap(req)).
		ParseFS(r.htmlDir,
			path.Join("src", "layouts", layout),
			path.Join("src", "pages", page),
		)
	if err != nil {
		return "", cerrors.New(err, "failed to parse templates in html dir", map[string]interface{}{
			"layout": layout,
			"page":   page,
		})
	}

	err = tmpl.Execute(&dest, data)
	if err != nil {
		return "", cerrors.New(err, "failed to execute template", nil)
	}

	// nolint:gosec
	return template.HTML(dest.String()), nil
}

func (r *HTMLRenderer) partial(req *http.Request) func(name string, data interface{}) (template.HTML, error) {
	return func(name string, data interface{}) (template.HTML, error) {
		return r.renderPartialFromDirWithFuncs("partials", name, r.funcMap(req), data)
	}
}

func (r *HTMLRenderer) livewireInitial(req *http.Request) func(name string, _ interface{}) (template.HTML, error) {
	return func(name string, _ interface{}) (template.HTML, error) {
		var id = crandom.GenerateRandomString(20)

		c, ok := r.livewireComponentByName[name]
		if !ok {
			return "", cerrors.New(nil, "component does not exist", map[string]interface{}{
				"name": name,
			})
		}

		initialDataRet := reflect.ValueOf(c).MethodByName("InitialData").Call(nil)
		if len(initialDataRet) != 1 {
			return "", cerrors.New(nil, "InitialData return value is invalid", nil)
		}

		data := initialDataRet[0].Interface()

		out, err := r.renderPartialFromDir(req, "livewire", name, data)
		if err != nil {
			return "", cerrors.New(err, "failed to execute html template", map[string]interface{}{
				"data": data,
			})
		}

		dataJ, err := json.Marshal(data)
		if err != nil {
			return "", cerrors.New(err, "failed to marshal data as json", nil)
		}

		initialData, err := json.Marshal(map[string]interface{}{
			"fingerprint": LivewireFingerprint{
				ID:               id,
				Name:             name,
				Locale:           "en",
				Path:             req.URL.Path,
				Method:           req.Method,
				InvalidationHash: "aaa",
			},
			"serverMemo": LivewireServerMemo{
				HTMLHash: htmlHash(out),
				Data:     dataJ,
				DataMeta: nil,
				Children: nil,
				Errors:   nil,
			},
			"effects": LivewireEffectsRequest{},
		})
		if err != nil {
			return "", cerrors.New(err, "failed to marshal initial data", nil)
		}

		html, err := updateHTML(out, map[string]string{
			"wire:id":           id,
			"wire:initial-data": string(initialData),
		}, fmt.Sprintf("<!-- Livewire Component wire-end:%s -->", id))
		if err != nil {
			return "", cerrors.New(err, "failed to render html", nil)
		}

		return html, nil
	}
}

func (r *HTMLRenderer) livewireUpdate(message *LivewireMessage) (*LivewireMessageResponse, error) {
	c, ok := r.livewireComponentByName[message.Fingerprint.Name]
	if !ok {
		return nil, cerrors.New(nil, "component does not exist", map[string]interface{}{
			"name": message.Fingerprint.Name,
		})
	}

	componentVal := reflect.ValueOf(c)
	dataType := componentVal.MethodByName("InitialData").Type().Out(0).Elem()

	dataVal := reflect.New(dataType)
	err := json.Unmarshal(message.ServerMemo.Data, dataVal.Interface())
	if err != nil {
		return nil, cerrors.New(err, "failed to unmarshal data into its type", map[string]interface{}{
			"data": string(message.ServerMemo.Data),
			"type": dataType.Name(),
		})
	}

	for i := range message.Updates {
		update := message.Updates[i]

		switch update.Type {
		case "callMethod":
			var payload LivewireUpdatePayloadCallMethod
			err := json.Unmarshal(update.Payload, &payload)
			if err != nil {
				return nil, cerrors.New(err, "failed to unmarshal payload", map[string]interface{}{
					"type":    update.Type,
					"payload": string(update.Payload),
				})
			}

			ret := componentVal.MethodByName(payload.Method).Call([]reflect.Value{
				dataVal,
			})

			if !ret[0].IsNil() {
				err = ret[0].Interface().(error)
				if err != nil {
					return nil, cerrors.New(err, "failed to call method on component", map[string]interface{}{
						"component": message.Fingerprint.Name,
						"payload":   payload,
					})
				}
			}
		case "syncInput":
			var payload LivewireUpdatePayloadSyncInput
			err := json.Unmarshal(update.Payload, &payload)
			if err != nil {
				return nil, cerrors.New(err, "failed to unmarshal payload", map[string]interface{}{
					"type":    update.Type,
					"payload": string(update.Payload),
				})
			}

			dataVal.Elem().FieldByName(payload.Name).Set(reflect.ValueOf(payload.Value))
		default:
			return nil, cerrors.New(nil, "unknown update type", map[string]interface{}{
				"type": update.Type,
			})
		}

	}

	initialReq, err := http.NewRequest(message.Fingerprint.Method, message.Fingerprint.Path, nil)
	if err != nil {
		return nil, cerrors.New(err, "failed to make initial http request", map[string]interface{}{
			"fingerprint": message.Fingerprint,
		})
	}

	out, err := r.renderPartialFromDir(initialReq, "livewire", message.Fingerprint.Name, dataVal.Interface())
	if err != nil {
		return nil, cerrors.New(err, "failed to execute html template", map[string]interface{}{
			"data": dataVal.Interface(),
		})
	}

	dataJ, err := json.Marshal(dataVal.Interface())
	if err != nil {
		return nil, cerrors.New(err, "failed to marshal data as json", nil)
	}

	updatedHTMLHash := htmlHash(out)

	effects := LivewireEffectsResponse{
		Dirty: make([]string, 0),
	}
	if message.ServerMemo.HTMLHash != updatedHTMLHash {
		var (
			dataPre  map[string]interface{}
			dataPost map[string]interface{}
		)

		err = json.Unmarshal(message.ServerMemo.Data, &dataPre)
		if err != nil {
			return nil, cerrors.New(err, "failed to unmarshal data pre", nil)
		}

		err = json.Unmarshal(dataJ, &dataPost)
		if err != nil {
			return nil, cerrors.New(err, "failed to unmarshal data post", nil)
		}

		for k, v := range dataPre {
			vPost, ok := dataPost[k]
			if !ok {
				effects.Dirty = append(effects.Dirty, k)
				continue
			}

			if !reflect.DeepEqual(v, vPost) {
				effects.Dirty = append(effects.Dirty, k)
				continue
			}
		}

		html, err := updateHTML(out, map[string]string{
			"wire:id": message.Fingerprint.ID,
		}, "")
		if err != nil {
			return nil, cerrors.New(err, "failed to render html", nil)
		}

		effects.HTML = html
	}

	return &LivewireMessageResponse{
		Effects: effects,
		ServerMemo: LivewireServerMemo{
			HTMLHash: updatedHTMLHash,
			Data:     dataJ,
		},
	}, nil
}

func (r *HTMLRenderer) renderPartialFromDir(req *http.Request, dir, name string, data interface{}) (template.HTML, error) {
	return r.renderPartialFromDirWithFuncs(dir, name, r.funcMap(req), data)
}

func (r *HTMLRenderer) renderPartialFromDirWithFuncs(dir, name string, fnMap template.FuncMap, data interface{}) (template.HTML, error) {
	var dest strings.Builder

	tmpl, err := template.New(name+".html").
		Funcs(fnMap).
		ParseFS(r.htmlDir,
			path.Join("src", dir, "*.html"),
		)
	if err != nil {
		return "", cerrors.New(err, "failed to parse partial template", map[string]interface{}{
			"dir":  dir,
			"name": name,
		})
	}

	err = tmpl.Execute(&dest, data)
	if err != nil {
		return "", cerrors.New(err, "failed to execute partial template", map[string]interface{}{
			"dir":  dir,
			"name": name,
		})
	}

	// nolint:gosec
	return template.HTML(dest.String()), nil
}
