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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/resolver"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

var base = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func row(name string, finishedMin int, mut ...func(*store.Row)) store.Row {
	r := store.Row{
		UID: testUID(name), Name: name, Namespace: "default", TestRef: "t", Phase: "passed",
		Source: "api", FinishedAt: base.Add(time.Duration(finishedMin) * time.Minute),
	}
	for _, m := range mut {
		m(&r)
	}
	return r
}

func finishedCR(name string, finishedMin int) *testsv1alpha1.TestRun {
	cr := liveRun(name, testsv1alpha1.PhaseFailed)
	f := metav1.NewTime(base.Add(time.Duration(finishedMin) * time.Minute))
	cr.Status.FinishedAt = &f
	cr.Spec.Source = "api"
	return cr
}

func listPage(t *testing.T, h http.Handler, query string) ([]runEnvelope, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs"+query, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out []runEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out, rec.Header().Get(HeaderNextCursor)
}

func names(es []runEnvelope) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

// Walks every page; live runs only on page 1, finished runs strictly
// newest-first with no gaps or repeats — including a terminal CR that is
// not (yet) in the store and must land in its time slot, not on page 1.
func TestListRuns_KeysetPagesAcrossStoreAndCluster(t *testing.T) {
	rows := []store.Row{row("a", 50), row("b", 40), row("c", 30), row("e", 10)}
	objs := []client.Object{
		liveRun("live", testsv1alpha1.PhaseRunning),
		finishedCR("d", 20), // terminal, not persisted yet
	}
	s, _ := mkServer(t, objs...)
	s.Store = newFakeRunStore(rows...)
	h := s.Handler()

	p1, cur := listPage(t, h, "?limit=2")
	assert.Equal(t, []string{"live", "a", "b"}, names(p1))
	require.NotEmpty(t, cur)

	p2, cur := listPage(t, h, "?limit=2&after="+cur)
	assert.Equal(t, []string{"c", "d"}, names(p2), "live runs never repeat; cluster-only run in its slot")
	require.NotEmpty(t, cur)

	p3, cur := listPage(t, h, "?limit=2&after="+cur)
	assert.Equal(t, []string{"e"}, names(p3))
	assert.Empty(t, cur, "no next cursor on the last page")
}

func TestListRuns_SourceAndFinishedAfter(t *testing.T) {
	rows := []store.Row{
		row("ui-new", 50, func(r *store.Row) { r.Source = "ui" }),
		row("ui-old", 5, func(r *store.Row) { r.Source = "ui" }),
		row("cron", 40, func(r *store.Row) { r.Source = "cron" }),
	}
	s, _ := mkServer(t, liveRun("live", testsv1alpha1.PhaseRunning))
	s.Store = newFakeRunStore(rows...)
	h := s.Handler()

	got, _ := listPage(t, h, "?source=ui")
	assert.Equal(t, []string{"ui-new", "ui-old"}, names(got))

	got, _ = listPage(t, h, "?finishedAfter="+base.Add(30*time.Minute).Format(time.RFC3339))
	assert.Equal(t, []string{"ui-new", "cron"}, names(got), "finishedAfter excludes live and older runs")
}

func TestListRuns_BadParamsAre400(t *testing.T) {
	_, h := mkServer(t)
	for _, q := range []string{"?after=!!!", "?after=bm90LWEtY3Vyc29y", "?finishedAfter=yesterday"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs"+q, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, q)
	}
}

func TestRunCursor_RoundTrip(t *testing.T) {
	ts := base.Add(123456789 * time.Nanosecond)
	c, err := decodeRunCursor(encodeRunCursor(ts, testUID("x")))
	require.NoError(t, err)
	assert.True(t, c.FinishedAt.Equal(ts))
	assert.Equal(t, testUID("x"), c.UID)
}

// --- envelope -----------------------------------------------------------

func TestRunEnvelope_CarriesRunContext(t *testing.T) {
	cr := liveRun("rich", testsv1alpha1.PhaseRunning)
	cr.Labels = map[string]string{store.LabelParentRun: "suite-run"}
	cr.Spec.Config = map[string]string{"vus": "50"}
	cr.Spec.Tags = map[string]string{TagCreatedBy: "alice@example.com"}
	cr.Spec.Abort = &testsv1alpha1.AbortRequest{Reason: testsv1alpha1.AbortReasonUser, RequestedBy: "bob"}
	cr.Status.Tool = "k6"
	cr.Status.ResolvedSpec = `{"config":{"vus":{"type":"integer","default":"10"},"duration":{"type":"string","default":"1m"}}}`
	cr.Status.TestCounts = &testsv1alpha1.TestCounts{Total: 3, Passed: 2, Failed: 1}
	cr.Status.Metrics = map[string]string{"p95_ms": "120.5", "garbage": "n/a"}
	cr.Status.Steps = map[string]testsv1alpha1.StepResult{"s0": {Phase: "failed", Message: "step timeout exceeded"}}

	e := runEnvelopeFromCR(cr)
	assert.Equal(t, "k6", e.Tool)
	assert.Equal(t, "suite-run", e.ParentRun)
	assert.Equal(t, map[string]string{"vus": "50", "duration": "1m"}, e.Config)
	assert.Equal(t, "alice@example.com", e.Tags[TagCreatedBy])
	assert.Equal(t, 1, e.TestCounts.Failed)
	assert.Equal(t, map[string]float64{"p95_ms": 120.5}, e.Metrics, "unparseable metrics dropped")
	assert.Equal(t, "step timeout exceeded", e.Steps["s0"].Message)
	assert.Equal(t, "bob", e.Abort.RequestedBy)
}

func TestRunEnvelope_FromArchivedRow(t *testing.T) {
	r := row("old", 1, func(r *store.Row) {
		r.Tool = "jmeter"
		r.Config = map[string]string{"threads": "20"}
		r.Steps = map[string]any{"s0": map[string]any{"phase": "passed", "message": "ok"}}
	})
	e := runEnvelopeFromRow(&r)
	assert.Equal(t, "jmeter", e.Tool)
	assert.Equal(t, "20", e.Config["threads"])
	assert.Equal(t, "ok", e.Steps["s0"].Message)
}

func TestRunEnvelope_GitCheckout(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	cr := liveRun("git", testsv1alpha1.PhasePassed)
	cr.Status.ResolvedSpec = `{"content":{"git":{"uri":"https://bot:s3cret@github.com/acme/tests.git"}}}`
	cr.Status.Content = &testsv1alpha1.ContentStatus{GitRevision: "main", GitCommit: sha}
	want := &apiclient.GitCheckout{URI: "https://github.com/acme/tests.git", Revision: "main", Commit: sha}
	assert.Equal(t, want, runEnvelopeFromCR(cr).Git, "credentials never leave the API server")

	r := row("old", 1, func(r *store.Row) {
		r.ResolvedSpec = map[string]any{"content": map[string]any{"git": map[string]any{"uri": "https://bot:s3cret@github.com/acme/tests.git"}}} // #nosec G101 -- fake credential, asserted to be stripped
		r.GitRevision, r.GitCommit = "main", sha
	})
	assert.Equal(t, want, runEnvelopeFromRow(&r).Git)

	assert.Nil(t, runEnvelopeFromCR(liveRun("plain", testsv1alpha1.PhasePassed)).Git, "no git source")
	plain := row("plain", 1)
	assert.Nil(t, runEnvelopeFromRow(&plain).Git)
	assert.Equal(t, "git@github.com:acme/tests.git", stripUserinfo("git@github.com:acme/tests.git"), "scp-style URIs pass through")
}

func TestCreateRun_RecordsCreatorFromHeader(t *testing.T) {
	s, h := mkServer(t)
	body := `{"metadata":{"name":"r1"},"spec":{"testRef":"t","tags":{"kubetest.io/created-by":"spoofed"}}}`
	req := httptest.NewRequest(http.MethodPost, "/runs", strings.NewReader(body))
	req.Header.Set(HeaderUser, "alice@example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var got testsv1alpha1.TestRun
	require.NoError(t, s.K8sClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "r1"}, &got))
	assert.Equal(t, "alice@example.com", got.Spec.Tags[TagCreatedBy], "header wins over payload")
}

// --- /tests/{name}/resolved -------------------------------------------

func TestResolvedTest_MergesTemplates(t *testing.T) {
	test := mkTest("load", "gitops")
	test.Spec.Use = []string{"k6"}
	test.Spec.Container = testsv1alpha1.ContainerConfig{} // image + args come from the template
	test.Spec.Config = map[string]testsv1alpha1.Parameter{"vus": {Type: "integer", Default: "5"}}
	s, _ := mkServer(t, test)
	s.Templates = resolver.MapStore{"default/k6": &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "k6", Namespace: "default", Labels: map[string]string{store.LabelTool: "k6"}},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Args: []string{"run", "{{ config.script }}"}},
			Config: map[string]testsv1alpha1.Parameter{
				"script": {Type: "string", Description: "Script path"},
				"vus":    {Type: "integer", Default: "1"},
			},
		},
	}}

	rec := get(t, s.Handler(), "/tests/load/resolved")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got resolvedTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "k6", got.Tool)
	assert.True(t, got.GitOpsLocked)
	assert.Equal(t, []string{"k6"}, got.Templates)
	assert.Equal(t, "grafana/k6:1.4.0", got.Spec.Container.Image)
	assert.Equal(t, "Script path", got.Spec.Config["script"].Description, "template-only param present")
	assert.Equal(t, "5", got.Spec.Config["vus"].Default, "Test wins over template")
	assert.Equal(t, []string{"run", "{{ config.script }}"}, got.Spec.Container.Args, "not evaluated")
}

func TestResolvedTest_Errors(t *testing.T) {
	broken := mkTest("broken", ManagedByUI)
	broken.Spec.Use = []string{"missing"}
	s, _ := mkServer(t, broken)
	s.Templates = resolver.MapStore{}
	assert.Equal(t, http.StatusUnprocessableEntity, get(t, s.Handler(), "/tests/broken/resolved").Code)
	assert.Equal(t, http.StatusNotFound, get(t, s.Handler(), "/tests/nope/resolved").Code)
}
