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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/cctest"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

const runAPI = "/api/v1/clusters/dev/runs/team-a/"

// runWorld is the API with run history, test cases and live links.
type runWorld struct {
	*world
	objects *storage.Fake
}

func newRunWorld(t *testing.T, archive *cctest.Archive, objs ...client.Object) *runWorld {
	t.Helper()
	o := cctest.Options{Cases: cctest.Cases{}}
	if archive != nil {
		o.Store, o.Deleter, o.Commenter = archive, archive, archive
	}
	env := cctest.New(t, o, objs...)
	s := &Server{Clusters: env.Clusters, Log: discard(),
		LiveURL: func(cluster, ns, run, worker, entry string) string {
			if worker != "" {
				run += "-w" + worker
			}
			return "/live/signed-" + cluster + "-" + ns + "-" + run + entry
		}}
	mux := http.NewServeMux()
	s.Register(mux)
	return &runWorld{world: &world{h: mux, k8s: env.K8s}, objects: env.Objects}
}

// finished is a finished run of smoke with the shared UID (cctest.Cases).
func finished(name string, phase testsv1alpha1.Phase) *testsv1alpha1.TestRun {
	r := runOf(name, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	r.UID = types.UID(cctest.RunUID)
	r.Status.Phase = phase
	r.Status.Message = "1 of 2 workers failed"
	r.Status.Steps = map[string]testsv1alpha1.StepResult{
		"worker-10": {Phase: "passed"}, "worker-2": {Phase: "failed", Message: "exit code 1",
			Params: map[string]string{"greeting": "hi"},
			Stages: []testsv1alpha1.Stage{{Kind: testsv1alpha1.StageRun, Name: "Run k6", Phase: "failed"}}}, "attempt-1": {Phase: "failed"},
	}
	r.Status.ArtifactRefs = []testsv1alpha1.ArtifactRef{
		{Path: "report/index.html", ContentType: "text/html"},
		{Path: "cypress/screenshots/pay (failed).png", ContentType: "image/png"},
		{Path: "cypress/videos/pay.mp4"},
	}
	r.Spec.Config = map[string]string{"vus": "50"}
	return r
}

// live is a running run of smoke (smoke-live).
func live() *testsv1alpha1.TestRun {
	r := finished("smoke-live", testsv1alpha1.PhaseRunning)
	r.Status.FinishedAt = nil
	return r
}

func TestGetRun_Finished(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	run := finished("smoke-abcde", testsv1alpha1.PhaseFailed)
	run.Status.ResolvedSpec = `{"artifacts":{"report":"report/index.html"},` +
		`"content":{"git":{"uri":"https://bot:s3cret@github.com/acme/tests.git"}}}`
	run.Status.Content = &testsv1alpha1.ContentStatus{GitRevision: "main", GitCommit: sha}
	run.Status.TestCounts = &testsv1alpha1.TestCounts{Total: 5, Passed: 3, Failed: 2}
	run.Status.Metrics = map[string]string{"p95_ms": "120.5", "errors": "0"}
	w := newRunWorld(t, nil, smokeTest(), run)

	rec := w.do(t, http.MethodGet, runAPI+"smoke-abcde", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "s3cret")
	got := decodeAs[RunDetail](t, rec)
	assert.Equal(t, "smoke-abcde", got.ID)
	assert.True(t, got.Finished)
	assert.False(t, got.Archived)
	assert.Equal(t, "1 of 2 workers failed", got.Message)
	assert.Equal(t, cctest.Developer, got.CreatedBy)
	assert.Equal(t, "report/index.html", got.Report)
	assert.Empty(t, got.LiveView, "finished: no live view")
	require.NotNil(t, got.Git)
	assert.Equal(t, "main", got.Git.Revision)
	assert.Equal(t, "https://github.com/acme/tests.git", got.Git.Repository)
	assert.Equal(t, Commit{SHA: sha, URL: "https://github.com/acme/tests/commit/" + sha}, got.Git.Commit)
	assert.Equal(t, &TestCounts{Total: 5, Passed: 3, Failed: 2}, got.TestCounts)
	assert.Equal(t, "50", got.Config["vus"])

	keys := make([]string, 0, len(got.Steps))
	for _, s := range got.Steps {
		keys = append(keys, s.Kind+":"+s.Key)
	}
	assert.Equal(t, []string{"Try:attempt-1", "Worker:worker-2", "Worker:worker-10"}, keys, "natural order")
	assert.Equal(t, map[string]string{"greeting": "hi"}, got.Steps[1].Params, "a worker's matrix values")
	assert.Equal(t, []Stage{{Kind: "run", Name: "Run k6", Phase: "failed"}}, got.Steps[1].Stages, "a worker's own stages")

	assert.Len(t, got.Artifacts, 3)
	assert.Equal(t, []Media{
		{Path: "cypress/screenshots/pay (failed).png", Type: "image/png"},
		{Path: "cypress/videos/pay.mp4", Type: "video/mp4", Video: true},
	}, got.Media)

	require.Len(t, got.FailedCases, 2, "only failed cases")
	assert.Equal(t, "checkout", got.FailedCases[0].Name)
	assert.Equal(t, "shop › checkout", got.FailedCases[0].Key)
	assert.Equal(t, "timeout after 5s", got.FailedCases[0].Message)
	assert.Equal(t, "at checkout", got.FailedCases[0].Details)
	assert.Empty(t, got.Children, "not composite")

	missing := w.do(t, http.MethodGet, runAPI+"nope", viewer, nil)
	assert.Equal(t, http.StatusNotFound, missing.Code)
}

func TestGetRun_LiveWithEvents(t *testing.T) {
	at := func(m int) metav1.Time { return metav1.NewTime(time.Date(2026, 10, 7, 12, m, 0, 0, time.UTC)) }
	ev := func(name, obj, typ, reason, msg string, m int) *corev1.Event {
		return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: obj},
			Type:           typ, Reason: reason, Message: msg, LastTimestamp: at(m), Count: 1}
	}
	run := live()
	run.Status.Steps = nil // a leaf run: the live view is its own
	run.Status.PodIP = "10.0.0.7"
	run.Status.ResolvedSpec = `{"liveView":{"port":5665,"path":"/ui/"}}`
	w := newRunWorld(t, nil, smokeTest(), run,
		ev("e1", "smoke-live-abc12", corev1.EventTypeNormal, "Scheduled", "Successfully assigned", 1),
		ev("e2", "smoke-live-abc12", corev1.EventTypeWarning, "Failed", "ErrImagePull", 2),
		ev("e3", "other-xyz", corev1.EventTypeNormal, "Pulling", "not ours", 3))

	got := decodeAs[RunDetail](t, w.do(t, http.MethodGet, runAPI+"smoke-live", viewer, nil))
	assert.False(t, got.Finished)
	assert.Equal(t, "/live/signed-dev-team-a-smoke-live/ui/", got.LiveView)
	require.Len(t, got.Events, 2, "only the run's objects")
	assert.Equal(t, "Scheduled", got.Events[0].Reason, "oldest first")
	assert.Equal(t, "Warning", got.Events[1].Type)
	assert.Empty(t, got.FailedCases, "not before the run ends")
}

func TestGetRun_CompositeChildren(t *testing.T) {
	parent := finished("suite-abcde", testsv1alpha1.PhasePassed)
	parent.Status.Steps = map[string]testsv1alpha1.StepResult{"s0": {Phase: "passed"}, "s1": {Phase: "passed"}}
	child := func(name string) *testsv1alpha1.TestRun {
		c := runOf(name, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
		c.Labels = map[string]string{store.LabelParentRun: "suite-abcde"}
		return c
	}
	stray := runOf("other-1", time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	w := newRunWorld(t, nil, smokeTest(), parent, child("suite-abcde-s10"), child("suite-abcde-s2"), stray)

	got := decodeAs[RunDetail](t, w.do(t, http.MethodGet, runAPI+"suite-abcde", viewer, nil))
	require.Len(t, got.Children, 2)
	assert.Equal(t, "suite-abcde-s2", got.Children[0].Name, "natural order")
	assert.Equal(t, "suite-abcde-s10", got.Children[1].Name)
	assert.Equal(t, "smoke", got.Children[0].TestRef)
}

func TestRunLog_FromOffsetWithPhase(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest(), live())
	keys := storage.ForRun("team-a", cctest.RunUID)
	put := func(i uint64, s string) {
		require.NoError(t, w.objects.Put(context.Background(), cctest.Bucket, keys.LogChunk(i),
			bytes.NewReader([]byte(s)), int64(len(s)), "text/plain"))
	}
	put(0, "hello\n")
	put(1, "world\n")

	rec := w.do(t, http.MethodGet, runAPI+"smoke-live/log?offset=0", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "hello\nworld\n", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "running", rec.Header().Get("X-Run-Phase"))
	assert.Equal(t, "12", rec.Header().Get("X-Next-Offset"))
	assert.Empty(t, rec.Header().Get("X-More"))

	rec = w.do(t, http.MethodGet, runAPI+"smoke-live/log?offset=6", viewer, nil)
	assert.Equal(t, "world\n", rec.Body.String())
	rec = w.do(t, http.MethodGet, runAPI+"smoke-live/log?offset=junk", viewer, nil)
	assert.Equal(t, "hello\nworld\n", rec.Body.String(), "a bad offset reads from the start")

	assert.Equal(t, http.StatusNotFound, w.do(t, http.MethodGet, runAPI+"ghost/log", viewer, nil).Code)
}

func TestRunLog_LongLogComesInChunks(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest(), live())
	big := strings.Repeat("x", MaxLogChunk+10)
	require.NoError(t, w.objects.Put(context.Background(), cctest.Bucket,
		storage.ForRun("team-a", cctest.RunUID).LogChunk(0), strings.NewReader(big), int64(len(big)), "text/plain"))
	rec := w.do(t, http.MethodGet, runAPI+"smoke-live/log", viewer, nil)
	assert.Len(t, rec.Body.String(), MaxLogChunk)
	assert.Equal(t, "1", rec.Header().Get("X-More"))
}

func TestAbortRun(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest(), live(),
		finished("smoke-done", testsv1alpha1.PhasePassed))
	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodPost, runAPI+"smoke-live/abort", viewer, AbortRequest{}).Code)

	rec := w.do(t, http.MethodPost, runAPI+"smoke-live/abort", developer, AbortRequest{Message: " wrong env "})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	var r testsv1alpha1.TestRun
	require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "smoke-live"}, &r))
	require.NotNil(t, r.Spec.Abort)
	assert.Equal(t, cctest.Developer, r.Spec.Abort.RequestedBy)
	assert.Equal(t, "wrong env", r.Spec.Abort.Message)

	done := w.do(t, http.MethodPost, runAPI+"smoke-done/abort", developer, AbortRequest{})
	assert.Equal(t, http.StatusConflict, done.Code, "a finished run can't be aborted")
	assert.NotEmpty(t, errorOf(t, done))
}

func TestRerun(t *testing.T) {
	old := finished("smoke-abcde", testsv1alpha1.PhaseFailed)
	old.Spec.Config = map[string]string{"vus": "50", "target": "https://shop", "gone": "x"}
	orphan := finished("orphan-1", testsv1alpha1.PhasePassed)
	orphan.UID, orphan.Spec.TestRef = "orphan-uid", "deleted-test"
	needs := finished("needs-1", testsv1alpha1.PhasePassed)
	needs.UID, needs.Spec.Config = "needs-uid", map[string]string{"vus": "5"}
	w := newRunWorld(t, nil, smokeTest(), baseTemplate(), old, orphan, needs)

	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodPost, runAPI+"smoke-abcde/rerun", viewer, nil).Code)
	rec := w.do(t, http.MethodPost, runAPI+"smoke-abcde/rerun", developer, nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	started := decodeAs[StartedRun](t, rec)
	var again testsv1alpha1.TestRun
	require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: started.Name}, &again))
	assert.Equal(t, "smoke", again.Spec.TestRef)
	assert.Equal(t, map[string]string{"vus": "50", "target": "https://shop"}, again.Spec.Config,
		"the old values; parameters the Test dropped are left out")
	assert.Equal(t, cctest.Developer, again.Spec.Tags[TagCreatedBy])

	gone := w.do(t, http.MethodPost, runAPI+"orphan-1/rerun", developer, nil)
	assert.Equal(t, http.StatusConflict, gone.Code)
	assert.Equal(t, "Test deleted-test no longer exists.", errorOf(t, gone))
	required := w.do(t, http.MethodPost, runAPI+"needs-1/rerun", developer, nil)
	assert.Equal(t, http.StatusConflict, required.Code)
	assert.Contains(t, errorOf(t, required), "now requires parameter target")
}

func TestRerun_FromHistory(t *testing.T) {
	archive := &cctest.Archive{Rows: map[string]store.Row{cctest.RunUID: {UID: cctest.RunUID, Name: "smoke-old",
		Namespace: "team-a", TestRef: "smoke", Phase: "passed", FinishedAt: time.Now().Add(-48 * time.Hour).UTC(),
		Config: map[string]string{"vus": "10", "target": "https://shop"}}}}
	w := newRunWorld(t, archive, smokeTest(), baseTemplate())

	got := decodeAs[RunDetail](t, w.do(t, http.MethodGet, runAPI+"smoke-old", viewer, nil))
	assert.True(t, got.Archived)
	assert.Equal(t, cctest.RunUID, got.ID, "history runs are found by UID")

	rec := w.do(t, http.MethodPost, runAPI+"smoke-old/rerun", developer, nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var runs testsv1alpha1.TestRunList
	require.NoError(t, w.k8s.List(context.Background(), &runs))
	require.Len(t, runs.Items, 1)
	assert.Equal(t, map[string]string{"target": "https://shop"}, runs.Items[0].Spec.Config, "vus=10 is today's default")
}

func TestComment_SaveAndRemove(t *testing.T) {
	archive := &cctest.Archive{Rows: map[string]store.Row{cctest.RunUID: {UID: cctest.RunUID, Name: "smoke-old",
		Namespace: "team-a", TestRef: "smoke", Phase: "failed", FinishedAt: time.Now().UTC()}}}
	w := newRunWorld(t, archive, smokeTest())
	rec := w.do(t, http.MethodPut, runAPI+"smoke-old/comment", developer, CommentUpdate{Text: " known flake "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	saved := decodeAs[RunComment](t, rec)
	assert.Equal(t, "known flake", saved.Text)
	assert.Equal(t, cctest.Developer, saved.By)
	got := decodeAs[RunDetail](t, w.do(t, http.MethodGet, runAPI+"smoke-old", viewer, nil))
	require.NotNil(t, got.Comment)
	assert.Equal(t, "known flake", got.Comment.Text)

	require.Equal(t, http.StatusNoContent, w.do(t, http.MethodDelete, runAPI+"smoke-old/comment", developer, nil).Code)
	assert.Nil(t, decodeAs[RunDetail](t, w.do(t, http.MethodGet, runAPI+"smoke-old", viewer, nil)).Comment)
}

func TestComment_Refused(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest(), finished("smoke-abcde", testsv1alpha1.PhaseFailed),
		live())
	assert.Equal(t, http.StatusForbidden,
		w.do(t, http.MethodPut, runAPI+"smoke-abcde/comment", viewer, CommentUpdate{Text: "x"}).Code)
	empty := w.do(t, http.MethodPut, runAPI+"smoke-abcde/comment", developer, CommentUpdate{Text: "  "})
	assert.Equal(t, http.StatusBadRequest, empty.Code)
	// No run history store behind this API server: the API refuses (503),
	// passed on as a bad gateway with its message.
	refused := w.do(t, http.MethodPut, runAPI+"smoke-abcde/comment", developer, CommentUpdate{Text: "known flake"})
	assert.Equal(t, http.StatusBadGateway, refused.Code)
	assert.Contains(t, errorOf(t, refused), "run history store is not configured")
	gone := w.do(t, http.MethodDelete, runAPI+"smoke-abcde/comment", developer, nil)
	assert.Equal(t, http.StatusBadGateway, gone.Code)
	junk := w.do(t, http.MethodPut, runAPI+"smoke-abcde/comment", developer, "not an object")
	assert.Equal(t, http.StatusBadRequest, junk.Code)
	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodDelete, runAPI+"smoke-abcde/comment", viewer, nil).Code)
}

func TestRunActions_UnknownRunAndCluster(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest())
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, runAPI + "ghost/abort"}, {http.MethodPost, runAPI + "ghost/rerun"},
		{http.MethodDelete, runAPI + "ghost"},
	} {
		rec := w.do(t, c.method, c.path, admin, AbortRequest{})
		assert.Equal(t, http.StatusNotFound, rec.Code, c.method+" "+c.path)
		other := w.do(t, c.method, strings.Replace(c.path, "/dev/", "/prod/", 1), admin, AbortRequest{})
		assert.Equal(t, http.StatusNotFound, other.Code, "unknown cluster")
	}
	assert.Equal(t, http.StatusBadRequest,
		w.do(t, http.MethodPost, runAPI+"ghost/abort", developer, "not an object").Code)
}

func TestDeleteRun(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest(), finished("smoke-abcde", testsv1alpha1.PhaseFailed))
	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodDelete, runAPI+"smoke-abcde", developer, nil).Code)
	require.Equal(t, http.StatusNoContent, w.do(t, http.MethodDelete, runAPI+"smoke-abcde", admin, nil).Code)
	assert.Equal(t, http.StatusNotFound, w.do(t, http.MethodGet, runAPI+"smoke-abcde", viewer, nil).Code)
}

// A parallel run has no live view of its own; each running worker has one.
func TestGetRun_ParallelWorkersLiveViews(t *testing.T) {
	run := live()
	run.Status.ResolvedSpec = `{"liveView":{"port":5665,"path":"/ui/"},"parallel":{"count":3}}`
	run.Status.Steps = map[string]testsv1alpha1.StepResult{
		"worker-0": {Phase: "passed"}, "worker-1": {Phase: "running"}, "worker-2": {Phase: "queued"},
	}
	w := newRunWorld(t, nil, smokeTest(), run)

	got := decodeAs[RunDetail](t, w.do(t, http.MethodGet, runAPI+"smoke-live", viewer, nil))
	assert.Empty(t, got.LiveView, "the run itself has no pod")
	require.Len(t, got.Steps, 3)
	assert.Empty(t, got.Steps[0].LiveView, "a finished worker's is gone")
	assert.Equal(t, "/live/signed-dev-team-a-smoke-live-w1/ui/", got.Steps[1].LiveView)
	assert.Equal(t, "/live/signed-dev-team-a-smoke-live-w2/ui/", got.Steps[2].LiveView, "waits for its worker")
}

// Each parallel worker's report reaches the UI with its step.
func TestGetRun_ParallelWorkersReports(t *testing.T) {
	run := finished("smoke-par", testsv1alpha1.PhasePassed)
	run.Status.ResolvedSpec = `{"artifacts":{"report":"r/k6-report.html"},"parallel":{"count":2}}`
	run.Status.ArtifactRefs = []testsv1alpha1.ArtifactRef{
		{Path: "workers/0/artifacts/r/k6-report.html"}, {Path: "workers/1/artifacts/r/k6-report.html"}}
	run.Status.Steps = map[string]testsv1alpha1.StepResult{"worker-0": {Phase: "passed"}, "worker-1": {Phase: "passed"}}
	w := newRunWorld(t, nil, smokeTest(), run)

	got := decodeAs[RunDetail](t, w.do(t, http.MethodGet, runAPI+"smoke-par", viewer, nil))
	assert.Equal(t, "workers/0/artifacts/r/k6-report.html", got.Report)
	require.Len(t, got.Steps, 2)
	assert.Equal(t, "workers/1/artifacts/r/k6-report.html", got.Steps[1].Report)
}
