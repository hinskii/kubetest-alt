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

// Package webhookdelivery contains the outbound webhook dispatcher +
// payload builder + retry logic. Split from internal/controller so a
// controller regression can't accidentally introduce a synchronous
// wait on the endpoint — the entire delivery pipeline runs on its
// own goroutine set with a bounded queue, and the controller's only
// entry point is a non-blocking Enqueue.
package webhookdelivery

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Payload is the JSON body every webhook receives. Shape is deliberately
// FLAT and STABLE across releases — external subscribers rely on it, so
// we treat it like an API. New fields are additive; renames or removals
// require a version bump on the payload.
type Payload struct {
	// Event is the run's lifecycle transition that triggered this
	// delivery — matches TestRun.Status.Phase.
	Event string `json:"event"`

	// RunName / RunNamespace / RunUID uniquely identify the TestRun.
	RunName      string `json:"runName"`
	RunNamespace string `json:"runNamespace"`
	RunUID       string `json:"runUID"`

	// TestRef is the Test the run executed.
	TestRef string `json:"testRef"`

	// Source (api/ui/cli/cron/trigger/gitops) records provenance so
	// downstream automation can filter by origin.
	Source string `json:"source,omitempty"`

	// Tool is the kubetest.io/tool label value (from the resolved
	// spec's pod/job labels). "" when the run isn't from the catalog.
	Tool string `json:"tool,omitempty"`

	// Phase is the terminal phase (passed/failed/error/aborted). Equal
	// to Event on terminal transitions; distinct for a queued/running
	// notification.
	Phase string `json:"phase"`

	// Message is the short human-readable status message the CR carries.
	Message string `json:"message,omitempty"`

	// QueuedAt / StartedAt / FinishedAt are RFC3339 timestamps. May be
	// empty if the transition preceded them (e.g. TestNotFound errors
	// before QueuedAt).
	QueuedAt   string `json:"queuedAt,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`

	// DurationMs is FinishedAt - StartedAt (or 0 when not both set).
	DurationMs int64 `json:"durationMs,omitempty"`

	// Metrics + TestCounts + ArtifactRefs mirror TestRun.Status. Present
	// only on terminal deliveries; nil on queued/running events.
	Metrics      map[string]string           `json:"metrics,omitempty"`
	TestCounts   *testsv1alpha1.TestCounts   `json:"testCounts,omitempty"`
	ArtifactRefs []testsv1alpha1.ArtifactRef `json:"artifactRefs,omitempty"`

	// Timestamp is when the dispatcher built the payload — useful for
	// receiver-side dedup / staleness checks.
	Timestamp string `json:"timestamp"`
}

// BuildPayload snapshots a TestRun into a stable webhook payload.
// `event` is the phase the caller wants the payload to advertise —
// controller calls with run.Status.Phase for terminal events, but on
// queued/started notifications the phase and event may differ.
// `now` is injected so tests get deterministic timestamps.
func BuildPayload(event string, run *testsv1alpha1.TestRun, tool string, now metav1.Time) Payload {
	p := Payload{
		Event:        event,
		Timestamp:    now.UTC().Format(nowFormat),
		Phase:        string(run.Status.Phase),
		RunName:      run.Name,
		RunNamespace: run.Namespace,
		RunUID:       string(run.UID),
		TestRef:      run.Spec.TestRef,
		Source:       run.Spec.Source,
		Tool:         tool,
		Message:      run.Status.Message,
		Metrics:      run.Status.Metrics,
		TestCounts:   run.Status.TestCounts,
		ArtifactRefs: run.Status.ArtifactRefs,
		DurationMs:   run.Status.DurationMs,
	}
	if run.Status.QueuedAt != nil {
		p.QueuedAt = run.Status.QueuedAt.UTC().Format(nowFormat)
	}
	if run.Status.StartedAt != nil {
		p.StartedAt = run.Status.StartedAt.UTC().Format(nowFormat)
	}
	if run.Status.FinishedAt != nil {
		p.FinishedAt = run.Status.FinishedAt.UTC().Format(nowFormat)
	}
	return p
}

// nowFormat is RFC3339 to the millisecond. Kept as a package const so
// tests + goldens agree on precision — the trailing "Z" reads as "UTC"
// unambiguously.
const nowFormat = "2006-01-02T15:04:05.000Z07:00"

// EventMatches reports whether the delivery should be attempted for
// the given event under a spec's Events filter. Empty filter = match
// everything (v1 semantics; extension via inversion/negation is a
// future rev).
func EventMatches(events []string, event string) bool {
	if len(events) == 0 {
		return true
	}
	return slices.Contains(events, event)
}
