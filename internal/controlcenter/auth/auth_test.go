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

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
)

func cfg(env string) *config.Config {
	return &config.Config{
		Environment: env,
		RBAC:        config.RBAC{Admins: []string{"boss@example.com"}, Developers: []string{"dev@example.com"}},
	}
}

func TestResolve(t *testing.T) {
	r, err := NewResolver(cfg(config.EnvProduction), "")
	require.NoError(t, err)
	assert.Equal(t, User{Email: "boss@example.com", Role: RoleAdmin}, r.Resolve(" Boss@Example.com "))
	assert.Equal(t, RoleDeveloper, r.Resolve("dev@example.com").Role)
	assert.Equal(t, RoleViewer, r.Resolve("someone@example.com").Role)
	assert.Equal(t, User{Role: RoleViewer}, r.Resolve(""))
}

func TestDevUser_RefusedInProduction(t *testing.T) {
	_, err := NewResolver(cfg(config.EnvProduction), "boss@example.com")
	require.Error(t, err)
}

func serve(t *testing.T, r *Resolver, h http.Handler, email string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if email != "" {
		req.Header.Set(HeaderEmail, email)
	}
	rec := httptest.NewRecorder()
	r.Middleware(h).ServeHTTP(rec, req)
	return rec
}

func TestMiddleware_DevUserOnlyWhenHeaderMissing(t *testing.T) {
	r, err := NewResolver(cfg(config.EnvDevelopment), "boss@example.com")
	require.NoError(t, err)
	var got User
	h := http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { got = FromContext(req.Context()) })

	serve(t, r, h, "")
	assert.Equal(t, RoleAdmin, got.Role)
	serve(t, r, h, "dev@example.com")
	assert.Equal(t, User{Email: "dev@example.com", Role: RoleDeveloper}, got, "the real header wins")
}

func TestMiddleware_EmailHeader(t *testing.T) {
	r, err := NewResolver(cfg(config.EnvProduction), "")
	require.NoError(t, err)
	r.EmailHeader = HeaderForwardedEmail // behind the chart's oauth2-proxy sidecar
	var got User
	h := http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { got = FromContext(req.Context()) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderForwardedEmail, "boss@example.com")
	req.Header.Set(HeaderEmail, "dev@example.com")
	r.Middleware(h).ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, User{Email: "boss@example.com", Role: RoleAdmin}, got, "only the configured header counts")
}

func TestRequire(t *testing.T) {
	r, err := NewResolver(cfg(config.EnvProduction), "")
	require.NoError(t, err)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := Require(RoleDeveloper, ok)

	assert.Equal(t, http.StatusForbidden, serve(t, r, h, "").Code)
	assert.Equal(t, http.StatusForbidden, serve(t, r, h, "someone@example.com").Code)
	assert.Equal(t, http.StatusNoContent, serve(t, r, h, "dev@example.com").Code)
	assert.Equal(t, http.StatusNoContent, serve(t, r, h, "boss@example.com").Code)
	assert.Contains(t, serve(t, r, h, "").Body.String(), "requires the developer role")
}
