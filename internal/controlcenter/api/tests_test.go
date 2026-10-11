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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/cctest"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/internal/store"
)

var (
	viewer    = auth.User{Email: "someone@example.com", Role: auth.RoleViewer}
	developer = auth.User{Email: cctest.Developer, Role: auth.RoleDeveloper}
	admin     = auth.User{Email: cctest.Admin, Role: auth.RoleAdmin}
)

const smokePath = "/api/v1/clusters/dev/tests/team-a/smoke"

// world is the API over a real kubetest API server (cctest).
type world struct {
	h   http.Handler
	k8s client.Client
}

func newWorld(t *testing.T, objs ...client.Object) *world {
	t.Helper()
	env := cctest.New(t, cctest.Options{}, objs...)
	return &world{h: handler(env.Clusters), k8s: env.K8s}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func handler(reg *clusters.Registry) http.Handler {
	mux := http.NewServeMux()
	(&Server{Clusters: reg, Log: discard()}).Register(mux)
	return mux
}

func (w *world) do(t *testing.T, method, path string, u auth.User, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req = req.WithContext(auth.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

func decodeAs[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeAs[Error](t, rec).Error
}

func smokeTest() *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "smoke", Namespace: "team-a",
			Labels: map[string]string{LabelTool: "k6", LabelManagedBy: ManagedByUI}},
		Spec: testsv1alpha1.TestSpec{
			Use:       []string{"base"},
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Args: []string{"run", "s.js"}},
			Config: map[string]testsv1alpha1.Parameter{
				"vus":    {Type: "integer", Default: "10"},
				"env":    {Type: "string", Default: "stage", Enum: []string{"stage", "prod"}},
				"target": {Type: "string", Pattern: "^https://"},
			},
		},
		Status: testsv1alpha1.TestStatus{LatestRun: &testsv1alpha1.RunReference{Name: "smoke-abcde", Phase: "failed"}},
	}
}

func baseTemplate() *testsv1alpha1.TestTemplate {
	return &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "team-a"},
		Spec:       testsv1alpha1.TestTemplateSpec{Config: map[string]testsv1alpha1.Parameter{"region": {Type: "string", Default: "eu"}}},
	}
}

func gitopsTest() *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "team-b", Labels: map[string]string{LabelTool: "cypress"}},
		Spec:       testsv1alpha1.TestSpec{Container: testsv1alpha1.ContainerConfig{Image: "cypress/included", Args: []string{"run"}}},
	}
}

func runOf(name string, started time.Time) *testsv1alpha1.TestRun {
	at := metav1.NewTime(started)
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID("uid-" + name)},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "smoke", Source: "ui",
			Tags: map[string]string{TagCreatedBy: cctest.Developer}},
		Status: testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhasePassed, StartedAt: &at, FinishedAt: &at},
	}
}

func TestListTests(t *testing.T) {
	notReady := gitopsTest()
	notReady.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse,
		Reason: "ParameterMissing", Message: "set spec.config.script: The k6 script.", LastTransitionTime: metav1.Now()}}
	unlabeled := gitopsTest()
	unlabeled.Name, unlabeled.Labels = "bare", nil
	w := newWorld(t, smokeTest(), notReady, unlabeled)

	rec := w.do(t, http.MethodGet, "/api/v1/clusters/dev/tests", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeAs[TestList](t, rec).Tests
	require.Len(t, got, 3)
	assert.Equal(t, []string{"team-a/smoke", "team-b/bare", "team-b/checkout"},
		[]string{got[0].Namespace + "/" + got[0].Name, got[1].Namespace + "/" + got[1].Name, got[2].Namespace + "/" + got[2].Name},
		"by namespace, then name")
	assert.Equal(t, TestSummary{Name: "smoke", Namespace: "team-a", Tool: "k6",
		LatestRun: &RunRef{Name: "smoke-abcde", Phase: "failed"}}, got[0])
	assert.Empty(t, got[1].Tool, "no tool label")
	assert.True(t, got[2].Locked, "not created by the GUI: read-only")
	assert.Equal(t, "set spec.config.script: The k6 script.", got[2].Problem)
	assert.Equal(t, "run history store is not configured", decodeAs[TestList](t, rec).StatsUnavailable,
		"no history: the list stands, saying why there are no stats")
}

type fakeStats struct{ since string }

func (f *fakeStats) TestStats(_ context.Context, sf store.StatsFilter) ([]store.TestStats, error) {
	f.since = time.Since(sf.Since).Round(time.Hour).String()
	ratio := []store.RecentRun{
		{UID: "u2", Name: "smoke-2", Phase: "failed", DurationMs: 2000, FinishedAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)},
		{UID: "u1", Name: "smoke-1", Phase: "passed", DurationMs: 1000, FinishedAt: time.Date(2026, 10, 11, 11, 0, 0, 0, time.UTC)},
	}
	return []store.TestStats{
		{Namespace: "team-a", TestRef: "smoke", Total: 4, Passed: 3, Failed: 1, P50Ms: 1000, P95Ms: 2000, Recent: ratio},
		{Namespace: "team-a", TestRef: "deleted-test", Total: 1, Passed: 1},
	}, nil
}

func TestListTests_Stats(t *testing.T) {
	stats := &fakeStats{}
	env := cctest.New(t, cctest.Options{Stats: stats}, smokeTest(), gitopsTest())
	w := &world{h: handler(env.Clusters), k8s: env.K8s}

	rec := w.do(t, http.MethodGet, "/api/v1/clusters/dev/tests?range=30d", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeAs[TestList](t, rec)
	assert.Equal(t, "30d", list.Range)
	assert.Equal(t, "720h0m0s", stats.since)
	assert.Empty(t, list.StatsUnavailable)
	require.Len(t, list.Tests, 2, "a deleted Test's history adds no row")
	smoke := list.Tests[0].Stats
	require.NotNil(t, smoke)
	require.NotNil(t, smoke.PassRatio)
	assert.InDelta(t, 0.75, *smoke.PassRatio, 1e-9)
	assert.Equal(t, int64(2000), smoke.P95Ms)
	assert.Equal(t, []string{"smoke-1", "smoke-2"}, []string{smoke.Recent[0].ID, smoke.Recent[1].ID}, "oldest first")
	assert.Nil(t, list.Tests[1].Stats, "no runs, no stats")

	rec = w.do(t, http.MethodGet, "/api/v1/clusters/dev/tests?range=bogus", viewer, nil)
	assert.Equal(t, "7d", decodeAs[TestList](t, rec).Range, "unknown range: the default")
}

func TestGetTest(t *testing.T) {
	test := smokeTest()
	test.Spec.Schedule = "0 2 * * *"
	w := newWorld(t, test, baseTemplate(), gitopsTest())

	rec := w.do(t, http.MethodGet, smokePath, viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeAs[TestDetail](t, rec)
	assert.Equal(t, "k6", got.Tool)
	assert.False(t, got.Locked)
	assert.Equal(t, []string{"base"}, got.Templates)
	assert.Equal(t, []Param{
		{Name: "env", Type: "string", Default: "stage", Enum: []string{"stage", "prod"}},
		{Name: "region", Type: "string", Default: "eu"},
		{Name: "target", Type: "string", Pattern: "^https://", Required: true},
		{Name: "vus", Type: "integer", Default: "10"},
	}, got.Params, "templates merged, by name")
	require.NotNil(t, got.Schedule)
	assert.Equal(t, "0 2 * * *", got.Schedule.Expr)
	assert.Len(t, got.Schedule.Next, NextFiresShown)
	assert.Contains(t, got.Definition, "image: grafana/k6:1.4.0")
	assert.Less(t, strings.Index(got.Definition, "use:"), strings.Index(got.Definition, "config:"),
		"reading order (yamlview), not alphabetical")

	locked := decodeAs[TestDetail](t, w.do(t, http.MethodGet, "/api/v1/clusters/dev/tests/team-b/checkout", viewer, nil))
	assert.True(t, locked.Locked)
	assert.Equal(t, []string{}, locked.Templates)
	assert.Equal(t, []Param{}, locked.Params)
	assert.Nil(t, locked.Schedule)

	missing := w.do(t, http.MethodGet, "/api/v1/clusters/dev/tests/team-a/nope", viewer, nil)
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.NotEmpty(t, errorOf(t, missing))
	noCluster := w.do(t, http.MethodGet, "/api/v1/clusters/prod/tests/team-a/smoke", viewer, nil)
	assert.Equal(t, http.StatusNotFound, noCluster.Code)
	assert.Contains(t, errorOf(t, noCluster), `No cluster named "prod"`)
}

func TestListTestRuns(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	git := runOf("smoke-git", base.Add(time.Minute))
	git.Status.ResolvedSpec = `{"content":{"git":{"uri":"https://bot:s3cret@github.com/acme/tests.git"}}}`
	git.Status.Content = &testsv1alpha1.ContentStatus{GitRevision: "main", GitCommit: sha}
	w := newWorld(t, smokeTest(), runOf("smoke-old", base), git)

	rec := w.do(t, http.MethodGet, smokePath+"/runs", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "s3cret")
	page := decodeAs[RunPage](t, rec)
	require.Len(t, page.Runs, 2)
	assert.Empty(t, page.Next, "one page")
	first := page.Runs[0]
	assert.Equal(t, "smoke-git", first.Name, "newest first")
	assert.Equal(t, "smoke-git", first.ID)
	assert.Equal(t, "passed", first.Phase)
	assert.Equal(t, cctest.Developer, first.CreatedBy)
	assert.Equal(t, "ui", first.Source)
	assert.Equal(t, &Commit{SHA: sha, URL: "https://github.com/acme/tests/commit/" + sha}, first.Commit)
	assert.Nil(t, page.Runs[1].Commit, "no git source, no commit")

	bad := w.do(t, http.MethodGet, smokePath+"/runs?after=garbage", viewer, nil)
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	assert.Contains(t, errorOf(t, bad), "malformed cursor")
}

func TestStartRun(t *testing.T) {
	w := newWorld(t, smokeTest(), baseTemplate())
	at := time.Date(2030, 1, 2, 3, 4, 0, 0, time.FixedZone("CET", 3600))
	rec := w.do(t, http.MethodPost, smokePath+"/runs", developer, RunRequest{
		Config:    map[string]string{"vus": "50", "env": "stage", "target": " https://shop "},
		NotBefore: &at,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	started := decodeAs[StartedRun](t, rec)
	assert.True(t, strings.HasPrefix(started.Name, "smoke-"), started.Name)
	assert.Equal(t, "team-a", started.Namespace)
	assert.Equal(t, at.UTC(), *started.NotBefore)

	var runs testsv1alpha1.TestRunList
	require.NoError(t, w.k8s.List(context.Background(), &runs))
	require.Len(t, runs.Items, 1)
	r := runs.Items[0]
	assert.Equal(t, map[string]string{"vus": "50", "target": "https://shop"}, r.Spec.Config, "defaults are not sent")
	assert.Equal(t, cctest.Developer, r.Spec.Tags[TagCreatedBy])
	require.NotNil(t, r.Spec.NotBefore)
	assert.Equal(t, at.UTC(), r.Spec.NotBefore.UTC())

	missing := w.do(t, http.MethodPost, smokePath+"/runs", developer, RunRequest{Config: map[string]string{"vus": "5"}})
	assert.Equal(t, http.StatusBadRequest, missing.Code)
	assert.Equal(t, "Parameter target is required.", errorOf(t, missing))
	unknown := w.do(t, http.MethodPost, smokePath+"/runs", developer,
		RunRequest{Config: map[string]string{"target": "https://x", "typo": "1"}})
	assert.Equal(t, http.StatusBadRequest, unknown.Code)
	assert.Equal(t, "The Test has no parameter typo.", errorOf(t, unknown))
	junk := w.do(t, http.MethodPost, smokePath+"/runs", developer, map[string]any{"config": map[string]string{}, "extra": 1})
	assert.Equal(t, http.StatusBadRequest, junk.Code, "unknown fields are refused")

	forbidden := w.do(t, http.MethodPost, smokePath+"/runs", viewer, RunRequest{})
	assert.Equal(t, http.StatusForbidden, forbidden.Code)
	assert.Contains(t, errorOf(t, forbidden), "requires the developer role")
}

func TestSetSchedule(t *testing.T) {
	w := newWorld(t, smokeTest(), gitopsTest())
	set := func(u auth.User, expr string) *httptest.ResponseRecorder {
		return w.do(t, http.MethodPut, smokePath+"/schedule", u, ScheduleUpdate{Schedule: expr})
	}
	stored := func() string {
		var got testsv1alpha1.Test
		require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "smoke"}, &got))
		return got.Spec.Schedule
	}

	assert.Equal(t, http.StatusForbidden, set(viewer, "0 2 * * *").Code, "viewers can't schedule")

	rec := set(developer, "  0   2 * * 1-5 ")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decodeAs[ScheduleResult](t, rec)
	require.NotNil(t, res.Schedule)
	assert.Equal(t, "0 2 * * 1-5", res.Schedule.Expr)
	assert.Len(t, res.Schedule.Next, NextFiresShown)
	assert.Equal(t, "0 2 * * 1-5", stored(), "whitespace normalized")

	bad := set(developer, "every night")
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	assert.Contains(t, errorOf(t, bad), "Not a valid cron expression")
	assert.Equal(t, "0 2 * * 1-5", stored(), "an invalid expression changes nothing")

	rec = set(developer, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, decodeAs[ScheduleResult](t, rec).Schedule)
	assert.Empty(t, stored())

	locked := w.do(t, http.MethodPut, "/api/v1/clusters/dev/tests/team-b/checkout/schedule", developer,
		ScheduleUpdate{Schedule: "0 2 * * *"})
	assert.Equal(t, http.StatusConflict, locked.Code)
	assert.Equal(t, "This Test is managed in Git: change spec.schedule there.", errorOf(t, locked))
}

func TestDeleteTest(t *testing.T) {
	w := newWorld(t, smokeTest(), gitopsTest())
	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodDelete, smokePath, developer, nil).Code)

	locked := w.do(t, http.MethodDelete, "/api/v1/clusters/dev/tests/team-b/checkout", admin, nil)
	assert.Equal(t, http.StatusConflict, locked.Code, "Git-managed Tests are deleted in Git")
	assert.NotEmpty(t, errorOf(t, locked))

	require.Equal(t, http.StatusNoContent, w.do(t, http.MethodDelete, smokePath, admin, nil).Code)
	err := w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "smoke"}, &testsv1alpha1.Test{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestClusterUnreachable(t *testing.T) {
	reg, err := clusters.New(t.Context(), []config.Cluster{{
		Name: "dev", Server: "http://127.0.0.1:1", Auth: config.ClusterAuth{Type: config.AuthServiceAccount},
		APIServer: config.APIServerRef{Namespace: "x", Service: "y", Port: 1},
	}}, clusters.Options{InClusterConfig: func() (*rest.Config, error) { return &rest.Config{}, nil }})
	require.NoError(t, err)
	w := &world{h: handler(reg)}
	rec := w.do(t, http.MethodGet, "/api/v1/clusters/dev/tests", viewer, nil)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "Cannot reach the kubetest API of dev.", errorOf(t, rec))
}
