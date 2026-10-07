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

package apiserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

const testToken = "0123456789abcdef0123456789abcdef-e2e"

// fixes.md #1: with a token configured, nothing but the probes and
// /metrics answers without it.
func TestAuthToken_Required(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil)
	require.NoError(t, s.K8sClient.Create(t.Context(), mkTest("smoke", "ui")))
	s.AuthToken = testToken
	s.MetricsHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.Handler()
	call := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.Header.Set(HeaderToken, token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, p := range []string{"/tests", "/tests/smoke", "/runs", "/runs/x/live/ui/", "/audit", "/openapi.json"} {
		rec := call(http.MethodGet, p, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code, p)
		assert.Contains(t, rec.Body.String(), apiclient.ReasonUnauthorized, p)
		assert.Equal(t, http.StatusUnauthorized, call(http.MethodGet, p, "wrong-token-of-the-same-length-xxxx").Code, p)
	}
	assert.Equal(t, http.StatusUnauthorized, call(http.MethodPost, "/runs", "").Code)
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/tests", testToken).Code)
	for _, p := range []string{"/healthz", "/metrics"} {
		assert.Equal(t, http.StatusOK, call(http.MethodGet, p, "").Code, "%s stays open for probes and Prometheus", p)
	}
}

func TestAuthToken_ClientSendsIt(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil)
	require.NoError(t, s.K8sClient.Create(t.Context(), mkTest("smoke", "ui")))
	s.AuthToken = testToken
	c := mkContractClient(t, s)

	_, err := c.ListTests(t.Context(), "")
	var apiErr *apiclient.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.Status)

	tests, err := c.WithToken(testToken).ListTests(t.Context(), "")
	require.NoError(t, err)
	assert.Len(t, tests, 1)
}
