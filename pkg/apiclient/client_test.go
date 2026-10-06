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

package apiclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The end-to-end contract against the real handler lives in
// internal/apiserver/apiclient_contract_test.go; these cover client-only
// behaviour.

func TestServiceProxyURL(t *testing.T) {
	assert.Equal(t,
		"https://k8s.example:443/api/v1/namespaces/kubetest/services/http:kubetest-apiserver:8080/proxy",
		ServiceProxyURL("https://k8s.example:443/", "kubetest", "kubetest-apiserver", 8080))
}

func TestNew_RejectsNonHTTP(t *testing.T) {
	_, err := New("ftp://x", nil)
	require.Error(t, err)
}

func TestRequest_PathEscapingAndQuery(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.EscapedPath(), r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	c, err := New(ts.URL+"/prefix/", ts.Client())
	require.NoError(t, err)

	_, err = c.ListArtifacts(t.Context(), "team-a", "run 1")
	require.NoError(t, err)
	assert.Equal(t, "/prefix/runs/run%201/artifacts", gotPath)
	assert.Equal(t, "namespace=team-a", gotQuery)

	after := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	_, err = c.ListRuns(t.Context(), ListRunsOptions{Test: "smoke", Limit: 10, FinishedAfter: &after})
	require.NoError(t, err)
	assert.Equal(t, "/prefix/runs", gotPath)
	assert.Equal(t, "finishedAfter=2026-01-02T02%3A04%3A05Z&limit=10&test=smoke", gotQuery)
}

func TestErrors_NonJSONBodyKeptAsMessage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream connect error", http.StatusBadGateway)
	}))
	defer ts.Close()
	c, err := New(ts.URL, ts.Client())
	require.NoError(t, err)

	_, err = c.GetRun(t.Context(), "", "x")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.Status)
	assert.Equal(t, "upstream connect error", apiErr.Message)
	assert.False(t, IsNotFound(err))
}
