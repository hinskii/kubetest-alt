/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package views holds Control Center's HTML templates and static assets,
// embedded into the binary. Server-rendered html/template; no frontend
// build step.
package views

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// Page is what every template receives.
type Page struct {
	Title string
	User  auth.User
	// Notice is a one-line result of the previous action (after a
	// POST-redirect-GET), shown above the content.
	Notice string
	// Breadcrumbs lead back up from this page.
	Breadcrumbs []Crumb
	// Data is the page-specific model.
	Data any
}

// Crumb is one breadcrumb link.
type Crumb struct {
	Label, Href string
}

// ErrorData is the model of the error page.
type ErrorData struct {
	Status     int
	StatusText string
	Message    string
}

// Views renders pages: each page template is parsed together with the
// layout, so every page defines "content" independently.
type Views struct {
	pages map[string]*template.Template
}

// New parses every page template once; a broken template fails startup.
func New() (*Views, error) {
	layout, err := template.New("layout.html").Funcs(funcs).ParseFS(templatesFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("views: layout: %w", err)
	}
	entries, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	v := &Views{pages: map[string]*template.Template{}}
	for _, path := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(path, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		t, err := template.Must(layout.Clone()).ParseFS(templatesFS, path)
		if err != nil {
			return nil, fmt.Errorf("views: %s: %w", name, err)
		}
		v.pages[name] = t
	}
	return v, nil
}

// Render writes page name with status. Rendering goes to a buffer first,
// so a template error becomes a clean 500 instead of half a page.
func (v *Views) Render(w http.ResponseWriter, status int, name string, p Page) error {
	t, ok := v.pages[name]
	if !ok {
		return fmt.Errorf("views: unknown page %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		return fmt.Errorf("views: render %s: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// Static serves the embedded assets; mount it at /static/.
func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embed path is fixed at compile time
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}
