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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/metrics"
)

// Defaults match the CRD's zero-value behavior. Kept exported so
// operators can reference them from cmd/operator when auditing config.
const (
	DefaultTimeoutSeconds     int32 = 10
	DefaultMaxRetries         int32 = 5
	DefaultBackoffBase              = 250 * time.Millisecond
	DefaultBackoffMax               = 30 * time.Second
	DefaultQueueBuffer              = 512
	DefaultWorkerCount              = 4
	RedactedHeaderPlaceholder       = "<redacted:secret>"
)

// SecretResolver resolves a Secret-backed header VALUE. The dispatcher
// stays free of a k8s client dependency by getting these through an
// injected function — production wires it via client-go; tests wire an
// in-memory map.
//
// Signature (namespace, secretName, key) → value; empty string + non-nil
// error → header is DROPPED from the outbound request (never sent as
// literal "") and the delivery attempt is marked failed. Prefer that
// over sending an empty auth header the endpoint may 401 on.
type SecretResolver func(namespace, secretName, key string) (string, error)

// OutcomeSink is the reconciler-facing callback the dispatcher invokes
// after each delivery attempt with the final outcome. Kept as an
// interface point so the controller can update Webhook.Status +
// metrics without the dispatcher importing either.
type OutcomeSink func(webhookKey NamespacedKey, outcome Outcome)

// NamespacedKey identifies a Webhook CR by (namespace, name). Not
// types.NamespacedName so this package doesn't need k8s deps for the
// tiny piece of state we track.
type NamespacedKey struct {
	Namespace string
	Name      string
}

// Outcome describes the terminal result of one delivery attempt sequence.
type Outcome struct {
	// Kind is one of the const strings below.
	Kind string

	// StatusCode is the HTTP status seen on the last attempt (or 0 if
	// no HTTP response arrived — connection error / timeout).
	StatusCode int

	// Attempts is how many attempts were made in total (1 + retries).
	Attempts int

	// Err carries the transport-level error when StatusCode==0.
	Err error
}

// Outcome kinds.
const (
	OutcomeSuccess       = "success"
	OutcomeFailedRetry   = "failed-after-retries" // 5xx / conn err after MaxRetries
	OutcomeNoRetry4xx    = "no-retry-4xx"         // 4xx = permanent, single attempt
	OutcomeTimeout       = "timeout"              // context deadline reached
	OutcomeSecretMissing = "secret-missing"       // SecretResolver returned an error
)

// Job is one queued delivery: a snapshot of the webhook spec + payload
// AT ENQUEUE TIME. If the CR is edited between enqueue and delivery, the
// delivered payload reflects the enqueue-time contract — no post-hoc
// swap of the destination URL or headers.
type Job struct {
	Key       NamespacedKey
	Spec      testsv1alpha1.WebhookSpec
	Namespace string // Secret namespace = webhook CR namespace
	Payload   Payload
	QueuedAt  time.Time
}

// Dispatcher owns the delivery goroutine pool + retry loop.
type Dispatcher struct {
	// Client is injectable so tests substitute httptest servers +
	// fault-injecting clients. cmd/operator wires an http.DefaultClient
	// with the default 0-timeout (per-attempt timeout comes from the
	// Job spec, applied via a per-request context).
	Client *http.Client

	// Secrets resolves Secret-backed header values on each attempt.
	// nil is treated as "unavailable" — ANY job that needs a secret
	// header fails with OutcomeSecretMissing rather than sending the
	// header empty.
	Secrets SecretResolver

	// OnOutcome is invoked once per Job after the terminal attempt
	// completes (success, permanent 4xx, or retries exhausted). May
	// be nil in tests that only care about HTTP-level behavior.
	OnOutcome OutcomeSink

	// Logger is the delivery pipeline's logger. All log lines that
	// include a header value MUST route through redactHeaders — the
	// grep-log security test asserts no bare secret ever appears.
	Logger logr.Logger

	// Now is time.Now overridable — tests use a fixed clock so backoff
	// assertions are deterministic. Defaults to time.Now.
	Now func() time.Time

	// QueueBuffer / WorkerCount tune the async pool. Zero → defaults.
	QueueBuffer int
	WorkerCount int

	// Backoff tuning — zero uses DefaultBackoffBase / DefaultBackoffMax.
	BackoffBase time.Duration
	BackoffMax  time.Duration

	// jobs is the async delivery queue. Buffered → Enqueue is non-
	// blocking as long as we're under capacity. Overflow drops the
	// oldest — see Enqueue.
	jobs   chan Job
	wg     sync.WaitGroup
	once   sync.Once
	closed chan struct{}
}

// Start initializes the worker pool. Must be called once before Enqueue.
// ctx cancellation drains outstanding jobs (each worker exits after its
// current attempt sequence). Safe to call multiple times — subsequent
// calls are no-ops.
func (d *Dispatcher) Start(ctx context.Context) {
	d.once.Do(func() {
		d.applyDefaults()
		d.jobs = make(chan Job, d.QueueBuffer)
		d.closed = make(chan struct{})
		for range d.WorkerCount {
			d.wg.Add(1)
			go d.worker(ctx)
		}
	})
}

// Stop waits for workers to drain outstanding jobs. Call once from the
// manager's shutdown path.
func (d *Dispatcher) Stop() {
	if d.jobs == nil {
		return
	}
	close(d.jobs)
	d.wg.Wait()
	close(d.closed)
}

// Enqueue puts j onto the delivery queue. NEVER BLOCKS: if the queue is
// full we drop the OLDEST pending job and enqueue the new one. Under
// steady load that gives the freshest events priority; under runaway
// load the dispatcher stays responsive at the cost of losing older
// deliveries (logged so alerts fire).
//
// The controller's calling contract: Enqueue in a defer or after status
// persist. NEVER await the delivery.
func (d *Dispatcher) Enqueue(j Job) {
	if d.jobs == nil {
		return
	}
	select {
	case d.jobs <- j:
	default:
		// Drop-oldest fallback so the queue drains under back-pressure.
		select {
		case <-d.jobs:
		default:
		}
		select {
		case d.jobs <- j:
		default:
		}
		d.Logger.Info("webhook queue full — dropped oldest to accept new job",
			"webhook", j.Key.Namespace+"/"+j.Key.Name,
			"event", j.Payload.Event)
	}
}

// applyDefaults fills in the zero-value knobs.
func (d *Dispatcher) applyDefaults() {
	if d.Client == nil {
		// The per-request context provides the actual timeout — set
		// the client's own to 0 so it never fights with our deadline.
		d.Client = &http.Client{}
	}
	if d.QueueBuffer == 0 {
		d.QueueBuffer = DefaultQueueBuffer
	}
	if d.WorkerCount == 0 {
		d.WorkerCount = DefaultWorkerCount
	}
	if d.BackoffBase == 0 {
		d.BackoffBase = DefaultBackoffBase
	}
	if d.BackoffMax == 0 {
		d.BackoffMax = DefaultBackoffMax
	}
	if d.Now == nil {
		d.Now = time.Now
	}
}

// worker drains the queue.
func (d *Dispatcher) worker(ctx context.Context) {
	defer d.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-d.jobs:
			if !ok {
				return
			}
			outcome := d.deliver(ctx, j)
			if d.OnOutcome != nil {
				d.OnOutcome(j.Key, outcome)
			}
		}
	}
}

// deliver runs the retry loop for one Job. Returns the terminal outcome.
func (d *Dispatcher) deliver(ctx context.Context, j Job) Outcome {
	timeout := time.Duration(j.Spec.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(DefaultTimeoutSeconds) * time.Second
	}
	// MaxRetries==0 is ambiguous in the CRD (unset int32 == 0 == "no
	// retries"). Convention: caller passes MaxRetries<0 to mean "use
	// default"; MaxRetries==0 means "no retries, one attempt".
	// Controller wire-up substitutes DefaultMaxRetries only when the
	// user left the field unset on the CR (nil pointer → -1 here).
	maxRetries := int(j.Spec.MaxRetries)
	if maxRetries < 0 {
		maxRetries = int(DefaultMaxRetries)
	}

	body, err := json.Marshal(j.Payload)
	if err != nil {
		// A marshal error is a programming bug — payload builder is
		// pure. Surface as OutcomeFailedRetry so the counter fires,
		// but don't retry (there's nothing to fix inside the dispatcher).
		return Outcome{Kind: OutcomeFailedRetry, Err: fmt.Errorf("marshal payload: %w", err), Attempts: 1}
	}

	// Resolve headers ONCE per delivery (not per attempt) — Secret values
	// are already fetched from the k8s API at build time, and re-fetching
	// per-retry would compound the attack surface for log leakage.
	headers, secretErr := d.resolveHeaders(j)
	if secretErr != nil {
		return Outcome{Kind: OutcomeSecretMissing, Err: secretErr, Attempts: 0}
	}

	backoff := d.BackoffBase
	var lastStatus int
	var lastErr error
	for attempt := 1; attempt <= maxRetries+1; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, j.Spec.URL, bytes.NewReader(body))
		if err != nil {
			cancel()
			return Outcome{Kind: OutcomeFailedRetry, Err: err, Attempts: attempt}
		}
		req.Header.Set("Content-Type", "application/json")
		for _, h := range headers {
			req.Header.Set(h.name, h.value)
		}
		resp, err := d.Client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			// Metric: transport error / timeout — code_class "err".
			metrics.WebhookDeliveriesTotal.WithLabelValues(metrics.CodeClass(0)).Inc()
			// Context timeout maps to a distinct outcome so operators
			// can distinguish endpoint slowness from endpoint errors.
			if errors.Is(err, context.DeadlineExceeded) {
				d.Logger.Info("webhook delivery timeout",
					"webhook", j.Key.Namespace+"/"+j.Key.Name,
					"attempt", attempt, "timeout", timeout)
				if attempt > maxRetries {
					return Outcome{Kind: OutcomeTimeout, Err: err, Attempts: attempt}
				}
			}
			d.Logger.Info("webhook delivery failed (network)",
				"webhook", j.Key.Namespace+"/"+j.Key.Name,
				"attempt", attempt, "err", err.Error())
			// Backoff before the next attempt (unless this was the last).
			if attempt > maxRetries {
				return Outcome{Kind: OutcomeFailedRetry, Err: err, Attempts: attempt}
			}
			d.backoffWait(ctx, &backoff)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		cancel()
		lastStatus = resp.StatusCode
		// Metric: bounded HTTP code-class label per attempt.
		metrics.WebhookDeliveriesTotal.WithLabelValues(metrics.CodeClass(resp.StatusCode)).Inc()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return Outcome{Kind: OutcomeSuccess, StatusCode: resp.StatusCode, Attempts: attempt}
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			// 4xx is permanent — the endpoint told us not to retry.
			// Do NOT log request headers here (secret leak); the log
			// line references only the URL and status.
			d.Logger.Info("webhook delivery permanent 4xx (no retry)",
				"webhook", j.Key.Namespace+"/"+j.Key.Name,
				"status", resp.StatusCode)
			return Outcome{Kind: OutcomeNoRetry4xx, StatusCode: resp.StatusCode, Attempts: attempt}
		}
		// 5xx / other → retry with backoff.
		d.Logger.Info("webhook delivery non-2xx (will retry)",
			"webhook", j.Key.Namespace+"/"+j.Key.Name,
			"attempt", attempt, "status", resp.StatusCode)
		if attempt > maxRetries {
			return Outcome{Kind: OutcomeFailedRetry, StatusCode: resp.StatusCode, Attempts: attempt}
		}
		d.backoffWait(ctx, &backoff)
	}
	return Outcome{Kind: OutcomeFailedRetry, StatusCode: lastStatus, Err: lastErr, Attempts: maxRetries + 1}
}

// backoffWait sleeps `*current` then doubles (capped at BackoffMax).
// Uses the injected context so shutdown cancellation cuts the wait.
func (d *Dispatcher) backoffWait(ctx context.Context, current *time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(*current):
	}
	*current *= 2
	if *current > d.BackoffMax {
		*current = d.BackoffMax
	}
}

// resolvedHeader is one (name, value) pair the dispatcher will send.
// Value is the RESOLVED value (secret contents when applicable); the
// dispatcher NEVER logs it directly.
type resolvedHeader struct {
	name   string
	value  string
	secret bool // true if sourced from a Secret — routed through redactHeaders
}

// resolveHeaders walks the webhook's header specs and produces the
// concrete (name, value) list. Inline values pass through verbatim;
// secret-backed values go through SecretResolver.
func (d *Dispatcher) resolveHeaders(j Job) ([]resolvedHeader, error) {
	out := make([]resolvedHeader, 0, len(j.Spec.Headers))
	for _, h := range j.Spec.Headers {
		if h.ValueFrom != nil && h.ValueFrom.SecretKeyRef != nil {
			if d.Secrets == nil {
				return nil, fmt.Errorf("webhook %s header %q references a Secret but no SecretResolver is wired",
					j.Key.Namespace+"/"+j.Key.Name, h.Name)
			}
			val, err := d.Secrets(j.Namespace, h.ValueFrom.SecretKeyRef.Name, h.ValueFrom.SecretKeyRef.Key)
			if err != nil {
				return nil, fmt.Errorf("resolve secret header %q: %w", h.Name, err)
			}
			out = append(out, resolvedHeader{name: h.Name, value: val, secret: true})
			continue
		}
		out = append(out, resolvedHeader{name: h.Name, value: h.Value})
	}
	return out, nil
}

// redactHeaders returns a copy of headers with secret-sourced values
// replaced by RedactedHeaderPlaceholder. Exported ONLY for the log-
// leak security test — production callers use it via logAttempt.
func redactHeaders(headers []resolvedHeader) []resolvedHeader {
	out := make([]resolvedHeader, len(headers))
	for i, h := range headers {
		if h.secret {
			out[i] = resolvedHeader{name: h.name, value: RedactedHeaderPlaceholder, secret: true}
			continue
		}
		out[i] = h
	}
	return out
}

// Ensure imports are used — corev1 is used by callers via WebhookHeader.
// Kept referenced so a future refactor that drops the header type
// import doesn't break this file.
var _ = corev1.EnvVarSource{}

// StringSet is a small helper for tests + callers that want a set-like
// membership check over headers.
type StringSet map[string]struct{}

// Contains reports membership.
func (s StringSet) Contains(v string) bool {
	_, ok := s[v]
	return ok
}

// NewStringSet builds a set from a slice.
func NewStringSet(xs []string) StringSet {
	out := make(StringSet, len(xs))
	for _, x := range xs {
		out[x] = struct{}{}
	}
	return out
}
