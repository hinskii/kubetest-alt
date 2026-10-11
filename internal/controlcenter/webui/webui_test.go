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

package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serve(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestIndex_NeverCached(t *testing.T) {
	rec := serve(Index(), "/")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	csp := rec.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self';", "scripts: same origin only")
	assert.NotContains(t, csp, "script-src")
	assert.Contains(t, csp, "style-src 'self' 'unsafe-inline'")
	if Built() {
		assert.Contains(t, rec.Body.String(), `<div id="root">`)
	} else {
		assert.Contains(t, rec.Body.String(), "make web-build")
	}
}

func TestNotFound_IsTheUIWithA404(t *testing.T) {
	rec := serve(NotFound(), "/nope")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, CSP, rec.Header().Get("Content-Security-Policy"))
	index := serve(Index(), "/").Body.String()
	assert.Equal(t, index, rec.Body.String(), "the UI renders its own not-found page")
}

func TestAssets_CachedForGood(t *testing.T) {
	if !Built() {
		t.Skip("UI not built (make web-build)")
	}
	entries, err := fs.ReadDir(sub(), "assets")
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	rec := serve(Assets(), AssetsPrefix+entries[0].Name())
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
}

func TestAssets_OnlyFilesUnderAssets(t *testing.T) {
	for _, p := range []string{"/assets/", "/assets/nope.js", "/index.html", "/.gitkeep"} {
		assert.Equal(t, http.StatusNotFound, serve(Assets(), p).Code, p)
	}
}

func TestTheme_RevalidatedScript(t *testing.T) {
	rec := serve(Theme(), ThemePath)
	if !Built() {
		assert.Equal(t, http.StatusNoContent, rec.Code, "no build: nothing to run, no error")
		return
	}
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "text/javascript; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `localStorage.getItem("theme")`)
}
