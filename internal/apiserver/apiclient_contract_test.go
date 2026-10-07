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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Contract tests: the real pkg/apiclient against the real handler, mounted
// under a service-proxy-shaped prefix — the path Control Center uses.
// A drift in routes, query names, headers or error shape fails here.

const proxyPrefix = "/api/v1/namespaces/kubetest/services/http:kubetest-apiserver:8080/proxy"

func mkContractClient(t *testing.T, s *Server) *apiclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(proxyPrefix+"/", http.StripPrefix(proxyPrefix, s.Handler()))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	c, err := apiclient.New(apiclient.ServiceProxyURL(ts.URL, "kubetest", "kubetest-apiserver", 8080), ts.Client())
	require.NoError(t, err)
	return c
}

func TestContract_TestsAndResolved(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil)
	require.NoError(t, s.K8sClient.Create(t.Context(), mkTest("smoke", "gitops")))
	c := mkContractClient(t, s)
	ctx := t.Context()

	tests, err := c.ListTests(ctx, "")
	require.NoError(t, err)
	require.Len(t, tests, 1)
	assert.Equal(t, "smoke", tests[0].Name)

	got, err := c.GetTest(ctx, "", "smoke")
	require.NoError(t, err)
	assert.Equal(t, "smoke", got.Name)

	resolved, err := c.GetResolvedTest(ctx, "", "smoke")
	require.NoError(t, err)
	assert.True(t, resolved.GitOpsLocked)
	require.NotNil(t, resolved.Spec)

	_, err = c.GetTest(ctx, "", "nope")
	require.Error(t, err)
	assert.True(t, apiclient.IsNotFound(err), err)
	var apiErr *apiclient.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apiclient.ReasonNotFound, apiErr.Reason)
}

func TestContract_SetTestSchedule(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil)
	require.NoError(t, s.K8sClient.Create(t.Context(), mkTest("nightly", "ui")))
	require.NoError(t, s.K8sClient.Create(t.Context(), mkTest("owned", "gitops")))
	c := mkContractClient(t, s)
	ctx := t.Context()

	got, err := c.SetTestSchedule(ctx, "", "nightly", "0 2 * * *")
	require.NoError(t, err)
	assert.Equal(t, "0 2 * * *", got.Spec.Schedule)
	assert.Equal(t, "ui", got.Labels[LabelManagedBy], "the rest of the Test is untouched")

	got, err = c.SetTestSchedule(ctx, "", "nightly", "")
	require.NoError(t, err)
	assert.Empty(t, got.Spec.Schedule, "an empty schedule clears it")

	_, err = c.SetTestSchedule(ctx, "", "owned", "0 2 * * *")
	require.Error(t, err)
	assert.True(t, apiclient.IsConflict(err), "a Git-managed Test is read-only: %v", err)
}

func TestContract_CreateRunRecordsUser(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil)
	c := mkContractClient(t, s).AsUser("alice@example.com")

	created, err := c.CreateRun(t.Context(), "", &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke"},
	})
	require.NoError(t, err)
	assert.Equal(t, "default", created.Namespace)
	assert.Equal(t, "ui", created.Spec.Source)
	assert.Equal(t, "alice@example.com", created.Spec.Tags[apiclient.TagCreatedBy])
}

func TestContract_ListRunsPagesWithCursor(t *testing.T) {
	rows := []store.Row{archivedRow("a"), archivedRow("b"), archivedRow("c")}
	for i := range rows {
		rows[i].FinishedAt = rows[i].FinishedAt.Add(-time.Duration(i) * time.Hour)
	}
	s, _, _ := mkStorageServer(t, rows, liveRun("live", testsv1alpha1.PhaseRunning))
	c := mkContractClient(t, s)
	ctx := t.Context()

	seen := map[string]bool{}
	opts := apiclient.ListRunsOptions{Limit: 2}
	for page := 0; ; page++ {
		require.Less(t, page, 5, "cursor never ended")
		p, err := c.ListRuns(ctx, opts)
		require.NoError(t, err)
		for _, r := range p.Runs {
			assert.False(t, seen[r.Name], "run %s returned twice", r.Name)
			seen[r.Name] = true
		}
		if p.NextCursor == "" {
			break
		}
		opts.After = p.NextCursor
	}
	assert.Equal(t, map[string]bool{"live": true, "a": true, "b": true, "c": true}, seen)

	p, err := c.ListRuns(ctx, apiclient.ListRunsOptions{Phase: "running"})
	require.NoError(t, err)
	require.Len(t, p.Runs, 1)
	assert.Equal(t, apiclient.OriginCluster, p.Runs[0].Origin)
}

func TestContract_GetAbortDeleteRun(t *testing.T) {
	s, _, _ := mkStorageServer(t, []store.Row{archivedRow("old")}, liveRun("r1", testsv1alpha1.PhaseRunning))
	c := mkContractClient(t, s).AsUser("bob@example.com")
	ctx := t.Context()

	old, err := c.GetRun(ctx, "", testUID("old"))
	require.NoError(t, err)
	assert.Equal(t, apiclient.OriginArchive, old.Origin)

	aborted, err := c.AbortRun(ctx, "", "r1", "wrong params")
	require.NoError(t, err)
	require.NotNil(t, aborted.Abort)
	assert.Equal(t, "bob@example.com", aborted.Abort.RequestedBy)

	var cr testsv1alpha1.TestRun
	require.NoError(t, s.K8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "r1"}, &cr))
	assert.Equal(t, "wrong params", cr.Spec.Abort.Message)

	_, err = c.AbortRun(ctx, "", testUID("old"), "")
	assert.True(t, apiclient.IsConflict(err), err)

	require.Error(t, c.DeleteRun(ctx, "", "r1"), "live runs can't be deleted")
	require.NoError(t, c.DeleteRun(ctx, "", testUID("old")))
	_, err = c.GetRun(ctx, "", testUID("old"))
	assert.True(t, apiclient.IsNotFound(err), err)
}

func TestContract_LogsAndArtifacts(t *testing.T) {
	s, up, _ := mkStorageServer(t, []store.Row{archivedRow("old")})
	up.put(storageKeysFor("old").LogChunk(0), []byte("line 1\n"))
	up.put(storageKeysFor("old").Artifact("report/index.html"), []byte("<h1>"))
	c := mkContractClient(t, s)
	ctx := t.Context()
	id := testUID("old")

	logs, err := c.OpenLogs(ctx, "", id)
	require.NoError(t, err)
	b, err := io.ReadAll(logs)
	require.NoError(t, err)
	require.NoError(t, logs.Close())
	assert.Equal(t, "line 1\n", string(b))

	arts, err := c.ListArtifacts(ctx, "", id)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, "report/index.html", arts[0].Path)

	st, err := c.OpenArtifact(ctx, "", id, arts[0].Path)
	require.NoError(t, err)
	b, err = io.ReadAll(st.Body)
	require.NoError(t, err)
	require.NoError(t, st.Body.Close())
	assert.Equal(t, "<h1>", string(b))
	assert.Equal(t, "text/html; charset=utf-8", st.ContentType)

	_, err = c.OpenArtifact(ctx, "", id, "missing.txt")
	assert.True(t, apiclient.IsNotFound(err), err)

	require.NoError(t, c.Healthz(ctx))
}
