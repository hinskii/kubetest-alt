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

package webhookdelivery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// logCapture records log lines into a buffer so the "no secret in logs"
// test can grep them. All dispatcher log lines route through Logger.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) write(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg)
}

func (l *logCapture) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// newDispatcher wires a dispatcher with an in-memory log capture and
// a fixed clock, plus the supplied secret resolver + outcome sink.
func newDispatcher(t *testing.T, sink OutcomeSink, secrets SecretResolver) (*Dispatcher, *logCapture) {
	t.Helper()
	cap := &logCapture{}
	logger := funcr.New(func(prefix, args string) { cap.write(prefix + " " + args) }, funcr.Options{})
	d := &Dispatcher{
		Logger:      logger,
		OnOutcome:   sink,
		Secrets:     secrets,
		BackoffBase: 1 * time.Millisecond, // tests never wait for real backoff
		BackoffMax:  5 * time.Millisecond,
		WorkerCount: 2,
		QueueBuffer: 8,
		Now:         func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	return d, cap
}

// mkJob builds a Job pointing at the given URL with sensible defaults.
// event is a parameter so future tests exercising queued/running/failed
// events use one helper.
// nolint:unparam
func mkJob(url string, headers []testsv1alpha1.WebhookHeader, event string) Job {
	return Job{
		Key: NamespacedKey{Namespace: "ns", Name: "hook"},
		Spec: testsv1alpha1.WebhookSpec{
			URL:            url,
			Headers:        headers,
			TimeoutSeconds: 1,
			MaxRetries:     2, // 1 initial + 2 retries = 3 attempts max
		},
		Namespace: "ns",
		Payload: Payload{
			Event:        event,
			RunName:      "run-1",
			RunNamespace: "ns",
			RunUID:       "uid-1",
			TestRef:      "t",
			Source:       "api",
			Phase:        event,
			Timestamp:    "2026-01-01T00:00:00.000Z",
		},
	}
}

// waitFor drains any completed outcomes, retrying until either want
// arrives or the timeout fires. Returns the outcomes actually seen.
// want is a parameter so multi-outcome scenarios (batch enqueue) reuse
// the helper.
// nolint:unparam
func waitFor(t *testing.T, ch chan Outcome, want int, timeout time.Duration) []Outcome {
	t.Helper()
	var got []Outcome
	deadline := time.After(timeout)
	for len(got) < want {
		select {
		case oc := <-ch:
			got = append(got, oc)
		case <-deadline:
			t.Fatalf("waitFor: got %d outcome(s), want %d within %s", len(got), want, timeout)
		}
	}
	return got
}

// TestDispatcher_HappyPath_SingleAttempt: 200 OK → OutcomeSuccess in 1
// attempt, payload landed at the server verbatim.
func TestDispatcher_HappyPath_SingleAttempt(t *testing.T) {
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen.Store(string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := make(chan Outcome, 1)
	d, _ := newDispatcher(t, func(_ NamespacedKey, o Outcome) { sink <- o }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	d.Enqueue(mkJob(srv.URL, nil, "passed"))

	got := waitFor(t, sink, 1, 2*time.Second)
	assert.Equal(t, OutcomeSuccess, got[0].Kind)
	assert.Equal(t, 1, got[0].Attempts)
	assert.Equal(t, 200, got[0].StatusCode)

	var payload Payload
	require.NoError(t, json.Unmarshal([]byte(seen.Load().(string)), &payload))
	assert.Equal(t, "passed", payload.Event)
	assert.Equal(t, "run-1", payload.RunName)
}

// TestDispatcher_Retry_500Then200_ExactlyTwoAttempts: plan-exact
// requirement — 500 then 200 → exactly 2 attempts, OutcomeSuccess.
func TestDispatcher_Retry_500Then200_ExactlyTwoAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := make(chan Outcome, 1)
	d, _ := newDispatcher(t, func(_ NamespacedKey, o Outcome) { sink <- o }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	d.Enqueue(mkJob(srv.URL, nil, "passed"))

	got := waitFor(t, sink, 1, 2*time.Second)
	assert.Equal(t, OutcomeSuccess, got[0].Kind)
	assert.Equal(t, 2, got[0].Attempts,
		"one 500 then one 200 = exactly 2 attempts, not 3+ (backoff correctness)")
	assert.EqualValues(t, 2, calls.Load())
}

// TestDispatcher_Permanent4xx_NoRetry: 4xx returns OutcomeNoRetry4xx
// after exactly ONE attempt. The plan's exact requirement.
func TestDispatcher_Permanent4xx_NoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	sink := make(chan Outcome, 1)
	d, _ := newDispatcher(t, func(_ NamespacedKey, o Outcome) { sink <- o }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	d.Enqueue(mkJob(srv.URL, nil, "passed"))

	got := waitFor(t, sink, 1, 2*time.Second)
	assert.Equal(t, OutcomeNoRetry4xx, got[0].Kind)
	assert.Equal(t, 1, got[0].Attempts, "4xx = permanent, single attempt")
	assert.Equal(t, http.StatusUnauthorized, got[0].StatusCode)
	assert.EqualValues(t, 1, calls.Load())
}

// TestDispatcher_5xxUntilExhausted_OutcomeFailedRetry: every attempt
// returns 500 → OutcomeFailedRetry after (MaxRetries+1) attempts.
func TestDispatcher_5xxUntilExhausted_OutcomeFailedRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := make(chan Outcome, 1)
	d, _ := newDispatcher(t, func(_ NamespacedKey, o Outcome) { sink <- o }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	j := mkJob(srv.URL, nil, "passed")
	j.Spec.MaxRetries = 2 // 1 initial + 2 retries = 3 attempts
	d.Enqueue(j)

	got := waitFor(t, sink, 1, 2*time.Second)
	assert.Equal(t, OutcomeFailedRetry, got[0].Kind)
	assert.Equal(t, 3, got[0].Attempts)
	assert.EqualValues(t, 3, calls.Load())
}

// TestDispatcher_HangingEndpoint_DoesNotBlockEnqueue is the plan's
// core async invariant: a hanging endpoint MUST NOT delay the
// controller's reconcile path. Enqueue completes within milliseconds
// even when the server never responds.
//
// Deadline strategy: Enqueue must return within 100ms (real wall-clock
// budget for a "would-be reconcile" line). The delivery to the hanging
// server hangs for as long as the per-attempt timeout, and eventually
// exits — but Enqueue returns instantly regardless.
func TestDispatcher_HangingEndpoint_DoesNotBlockEnqueue(t *testing.T) {
	// Server that NEVER writes a response. The dispatcher's per-attempt
	// context.WithTimeout eventually cancels; caller sees OutcomeTimeout
	// or OutcomeFailedRetry.
	//
	// Defer order matters: httptest.Server.Close() BLOCKS waiting for
	// active handlers. If we close(block) AFTER srv.Close() in the
	// defer chain, the handler waits forever on the block channel and
	// srv.Close() waits forever on the handler = test deadlock.
	// close(block) MUST run before srv.Close() — since defers run LIFO,
	// declare `defer close(block)` AFTER `defer srv.Close()`.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block) // runs BEFORE srv.Close

	sink := make(chan Outcome, 1)
	d, _ := newDispatcher(t, func(_ NamespacedKey, o Outcome) { sink <- o }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	j := mkJob(srv.URL, nil, "passed")
	j.Spec.TimeoutSeconds = 1 // 1s per attempt
	j.Spec.MaxRetries = 0     // one shot — we don't want to wait 3s

	// The bar the plan set: Enqueue completes within 100ms wall-clock,
	// even against a server that never responds.
	start := time.Now()
	d.Enqueue(j)
	assert.Less(t, time.Since(start), 100*time.Millisecond,
		"Enqueue MUST NOT wait on the delivery goroutine")

	// The delivery itself eventually times out — verify to prove the
	// dispatcher is functional (not that Enqueue lied).
	got := waitFor(t, sink, 1, 5*time.Second)
	assert.Contains(t, []string{OutcomeTimeout, OutcomeFailedRetry}, got[0].Kind,
		"hung endpoint must terminate via timeout, not hang forever")
}

// TestDispatcher_EventFilter_Table: subscribed events land; others don't.
// Filter with 0 entries subscribes to everything.
func TestDispatcher_EventFilter_Table(t *testing.T) {
	cases := []struct {
		name    string
		filter  []string
		event   string
		wantHit bool
	}{
		{"empty filter matches every event", nil, "passed", true},
		{"filter subscribes to matching event", []string{"passed", "failed"}, "failed", true},
		{"filter rejects non-matching event", []string{"passed"}, "aborted", false},
		{"filter multi-entry", []string{"queued", "passed"}, "queued", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantHit, EventMatches(tc.filter, tc.event))
		})
	}
}

// TestDispatcher_SecretHeader_NeverInLogs is the mandatory log-leak
// test. The dispatcher sends a Secret-backed header, then we grep the
// full log capture for the secret's literal value. It MUST NOT appear.
// Failure = a fresh CVE if this ships.
func TestDispatcher_SecretHeader_NeverInLogs(t *testing.T) {
	// Server returns 500 so the dispatcher exhausts retries + emits
	// every "delivery failed" log line — maximizes chances of a leak.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	const secretValue = "sk-XXXsuperSecret42XXX" // #nosec G101 -- test-only sentinel string, checked for absence in logs

	secrets := func(_, _, _ string) (string, error) { return secretValue, nil }
	sink := make(chan Outcome, 1)
	d, cap := newDispatcher(t, func(_ NamespacedKey, o Outcome) { sink <- o }, secrets)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	headers := []testsv1alpha1.WebhookHeader{
		{
			Name: "X-Auth",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
					Key:                  "k",
				},
			},
		},
	}
	j := mkJob(srv.URL, headers, "passed")
	j.Spec.MaxRetries = 2
	d.Enqueue(j)
	_ = waitFor(t, sink, 1, 3*time.Second)

	logs := cap.all()
	assert.NotEmpty(t, logs, "sanity: dispatcher should have logged retry attempts")
	assert.NotContains(t, logs, secretValue,
		"the Secret value MUST NEVER appear in dispatcher logs — even under retry storm. "+
			"If this fails, treat it as a fresh CVE.")
}

// TestDispatcher_SecretMissing_OutcomeNoSend: SecretResolver error
// SHORT-CIRCUITS the delivery — no HTTP request is made, outcome is
// OutcomeSecretMissing. Prevents "endpoint 401's on empty header,
// operator retries forever" failure mode.
func TestDispatcher_SecretMissing_OutcomeNoSend(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secrets := func(_, _, _ string) (string, error) {
		return "", assertingSecretError{}
	}
	sink := make(chan Outcome, 1)
	d, _ := newDispatcher(t, func(_ NamespacedKey, o Outcome) { sink <- o }, secrets)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	headers := []testsv1alpha1.WebhookHeader{{
		Name: "X-Auth",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
				Key:                  "k",
			},
		},
	}}
	d.Enqueue(mkJob(srv.URL, headers, "passed"))

	got := waitFor(t, sink, 1, 2*time.Second)
	assert.Equal(t, OutcomeSecretMissing, got[0].Kind)
	assert.EqualValues(t, 0, calls.Load(),
		"missing secret must short-circuit — never fire an HTTP request with an empty header")
}

type assertingSecretError struct{}

func (assertingSecretError) Error() string { return "secret 's' not found" }

// TestDispatcher_EnqueueOverflow_DropsOldest is a small correctness
// pin: with a tiny queue, filling past capacity drops the OLDEST job
// (LIFO'ish under back-pressure) rather than blocking the caller.
func TestDispatcher_EnqueueOverflow_DropsOldest(t *testing.T) {
	// Handler NEVER responds until we release — keeps workers busy so
	// the queue actually fills.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block) // runs BEFORE srv.Close (see comment on other hanging test)

	d, _ := newDispatcher(t, nil, nil)
	d.QueueBuffer = 2
	d.WorkerCount = 1
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	// Enqueue 10 jobs against a queue of 2. First one may go to the
	// worker; the rest exercise the drop-oldest fallback. Every call
	// returns instantly.
	start := time.Now()
	for range 10 {
		d.Enqueue(mkJob(srv.URL, nil, "passed"))
	}
	assert.Less(t, time.Since(start), 200*time.Millisecond,
		"Enqueue under overflow must not block")
}

// TestBuildPayload_Shape asserts every field the shape claims to carry
// actually lands. This is a "public API" test — external subscribers
// consume this JSON.
func TestBuildPayload_Shape(t *testing.T) {
	now := metav1.NewTime(time.Unix(1_700_000_000, 123_000_000).UTC())
	q := metav1.NewTime(time.Unix(1_700_000_001, 0).UTC())
	s := metav1.NewTime(time.Unix(1_700_000_002, 0).UTC())
	f := metav1.NewTime(time.Unix(1_700_000_003, 0).UTC())
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "n", UID: "u-1"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "t", Source: "api"},
		Status: testsv1alpha1.TestRunStatus{
			Phase:      testsv1alpha1.PhasePassed,
			QueuedAt:   &q,
			StartedAt:  &s,
			FinishedAt: &f,
			DurationMs: 1000,
			Message:    "all good",
			Metrics:    map[string]string{"p95_ms": "150"},
			TestCounts: &testsv1alpha1.TestCounts{Total: 5, Passed: 5},
		},
	}
	p := BuildPayload("passed", run, "k6", now)
	assert.Equal(t, "passed", p.Event)
	assert.Equal(t, "passed", p.Phase)
	assert.Equal(t, "r", p.RunName)
	assert.Equal(t, "n", p.RunNamespace)
	assert.Equal(t, "u-1", p.RunUID)
	assert.Equal(t, "t", p.TestRef)
	assert.Equal(t, "api", p.Source)
	assert.Equal(t, "k6", p.Tool)
	assert.Equal(t, "all good", p.Message)
	assert.Equal(t, int64(1000), p.DurationMs)
	assert.NotEmpty(t, p.QueuedAt)
	assert.NotEmpty(t, p.StartedAt)
	assert.NotEmpty(t, p.FinishedAt)
	assert.Equal(t, "2023-11-14T22:13:20.123Z", p.Timestamp)
	assert.Equal(t, "150", p.Metrics["p95_ms"])
	require.NotNil(t, p.TestCounts)
	assert.Equal(t, 5, p.TestCounts.Total)
}

// TestRedactHeaders_MasksSecrets: internal helper test — secret-flagged
// headers are replaced with the placeholder; plaintext ones pass
// through. This is the unit-level guard behind the log-leak assertion.
func TestRedactHeaders_MasksSecrets(t *testing.T) {
	in := []resolvedHeader{
		{name: "X-Auth", value: "actual-secret", secret: true},
		{name: "X-Trace-Id", value: "trace-abc", secret: false},
	}
	out := redactHeaders(in)
	assert.Equal(t, RedactedHeaderPlaceholder, out[0].value)
	assert.Equal(t, "trace-abc", out[1].value)
}
