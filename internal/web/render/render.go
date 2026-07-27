// Package render owns template parsing and the HTMX partial dispatch.
//
// The HX-Request branch lives here and nowhere else. Repeating it in every
// handler is how half of them end up returning a full page into a div.
package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Renderer holds the parsed template sets.
//
// Each page gets its own set containing the layout, that page, and every
// partial -- so a page can include any partial, and a partial can still be
// rendered standalone. That standalone property is the whole point of a
// partial: if it only works once its parent has rendered, it is not a partial.
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
	dev      bool
	fsys     fs.FS
	funcs    template.FuncMap
}

// New parses the template tree.
func New(fsys fs.FS, dev bool) (*Renderer, error) {
	r := &Renderer{dev: dev, fsys: fsys, funcs: funcs()}
	if err := r.parse(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Renderer) parse() error {
	partials, err := template.New("partials").Funcs(r.funcs).ParseFS(r.fsys, "templates/partials/*.html")
	if err != nil {
		return fmt.Errorf("parsing partials: %w", err)
	}
	r.partials = partials

	pageFiles, err := fs.Glob(r.fsys, "templates/pages/*.html")
	if err != nil {
		return fmt.Errorf("listing pages: %w", err)
	}
	if len(pageFiles) == 0 {
		return fmt.Errorf("parsing templates: no pages found")
	}

	pages := make(map[string]*template.Template, len(pageFiles))
	for _, pageFile := range pageFiles {
		name := strings.TrimSuffix(path.Base(pageFile), ".html")
		ts, err := template.New(name).Funcs(r.funcs).ParseFS(r.fsys,
			"templates/layouts/base.html",
			"templates/partials/*.html",
			pageFile,
		)
		if err != nil {
			return fmt.Errorf("parsing page %s: %w", name, err)
		}
		pages[name] = ts
	}
	r.pages = pages
	return nil
}

// Page renders a full page through the layout.
func (r *Renderer) Page(w http.ResponseWriter, status int, name string, data any) {
	if r.dev {
		// Reparsing per request makes template edits visible without a
		// restart. Never on in production: it is a filesystem read per hit.
		if err := r.parse(); err != nil {
			r.serverError(w, fmt.Errorf("reparsing templates: %w", err))
			return
		}
	}
	ts, ok := r.pages[name]
	if !ok {
		r.serverError(w, fmt.Errorf("rendering page: no such template %q", name))
		return
	}

	// Render into a buffer first. Writing straight to the ResponseWriter
	// means a template error halfway through produces a 200 with half a page,
	// which is worse than an honest 500.
	var buf bytes.Buffer
	if err := ts.ExecuteTemplate(&buf, "base", data); err != nil {
		r.serverError(w, fmt.Errorf("rendering page %s: %w", name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// Partial renders a single named partial.
func (r *Renderer) Partial(w http.ResponseWriter, status int, name string, data any) {
	if r.dev {
		if err := r.parse(); err != nil {
			r.serverError(w, fmt.Errorf("reparsing templates: %w", err))
			return
		}
	}
	var buf bytes.Buffer
	if err := r.partials.ExecuteTemplate(&buf, name, data); err != nil {
		r.serverError(w, fmt.Errorf("rendering partial %s: %w", name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// Respond is the HTMX dispatch: an HTMX request gets the partial, a plain
// browser navigation gets the whole page.
//
// Handlers call this instead of choosing for themselves, which is what keeps
// every route working with JavaScript disabled and makes deep links valid.
func (r *Renderer) Respond(w http.ResponseWriter, req *http.Request, status int, page, partial string, data any) {
	if IsHTMX(req) && partial != "" {
		r.Partial(w, status, partial, data)
		return
	}
	r.Page(w, status, page, data)
}

// IsHTMX reports whether the request came from HTMX.
func IsHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// Redirect sends the client to another page.
//
// HTMX will not follow a 302 the way a browser does -- it would swap the
// redirect target's body into the element that made the request. HX-Redirect
// tells it to perform a real navigation instead.
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	if IsHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// Trigger asks the client to fire an event. Client-side behaviour goes through
// this header rather than through inline script in a response body.
func Trigger(w http.ResponseWriter, event string) {
	w.Header().Set("HX-Trigger", event)
}

func (r *Renderer) serverError(w http.ResponseWriter, err error) {
	// The detail goes to the log, never to the client.
	logError(err)
	http.Error(w, "Something went wrong rendering this page.", http.StatusInternalServerError)
}

// PartialWithOOB renders a primary fragment followed by one or more
// out-of-band fragments in a single response.
//
// HTMX swaps the primary fragment into the requesting element's target and
// processes any element carrying hx-swap-oob wherever it appears in the body.
// That is what lets a handler report what happened without the caller having
// arranged anywhere to put the message.
func (r *Renderer) PartialWithOOB(w http.ResponseWriter, status int, name string, data any, oob ...OOB) {
	if r.dev {
		if err := r.parse(); err != nil {
			r.serverError(w, fmt.Errorf("reparsing templates: %w", err))
			return
		}
	}

	var buf bytes.Buffer
	if err := r.partials.ExecuteTemplate(&buf, name, data); err != nil {
		r.serverError(w, fmt.Errorf("rendering partial %s: %w", name, err))
		return
	}
	for _, extra := range oob {
		if err := r.partials.ExecuteTemplate(&buf, extra.Template, extra.Data); err != nil {
			r.serverError(w, fmt.Errorf("rendering out-of-band partial %s: %w", extra.Template, err))
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// OOB is one out-of-band fragment to append to a response.
type OOB struct {
	Template string
	Data     any
}
