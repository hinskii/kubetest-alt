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

// Package webui serves Control Center's React UI (web/, step 20R), built
// by `make web-build` into dist/ and embedded in the binary — one image,
// as before. Without a build (go test, go run without Node) the pages say
// how to build it instead of failing.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist is web/'s build output (vite outDir). all: keeps .gitkeep, so the
// directory exists in a fresh checkout.
//
//go:embed all:dist
var dist embed.FS

// AssetsPrefix is where the build's hashed files are served.
const AssetsPrefix = "/assets/"

// CSP is the UI document's Content-Security-Policy. Scripts stay
// same-origin only; inline styles are allowed because the component
// libraries (sonner, Radix) inject <style> elements at run time. Style
// injection can restyle a page but not run code; the server-rendered
// pages keep the stricter policy.
const CSP = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// notBuilt is served for UI routes when dist has no index.html.
const notBuilt = `<!doctype html><meta charset="utf-8"><title>Control Center</title>
<p>Control Center's UI isn't built into this binary. Run <code>make web-build</code>,
then build Control Center again (images do this in their web stage).</p>`

func sub() fs.FS {
	s, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // embed path is fixed at compile time
	}
	return s
}

// Built reports whether the UI was built into this binary.
func Built() bool {
	_, err := fs.Stat(sub(), "index.html")
	return err == nil
}

// Assets serves /assets/*: content-hashed names, so cached for good.
func Assets() http.Handler {
	files := http.FileServerFS(sub())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, AssetsPrefix) || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

// Index serves the UI's index.html for a route the UI owns; the browser
// router takes it from there. Never cached, so a new build shows at once.
func Index() http.Handler { return page(http.StatusOK) }

// NotFound serves index.html with 404 for an address nothing owns: the
// UI renders its not-found page, and the status stays honest.
func NotFound() http.Handler { return page(http.StatusNotFound) }

func page(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", CSP)
		b, err := fs.ReadFile(sub(), "index.html")
		if err != nil {
			b = []byte(notBuilt)
		}
		w.WriteHeader(status)
		_, _ = w.Write(b)
	})
}
