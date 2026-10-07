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

// Package apiclient is the typed Go client for the kubetest API server,
// and the single home of its wire types: internal/apiserver serves these
// exact types (it aliases them), so client and server can't drift.
package apiclient

import (
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Protocol constants shared with the server.
const (
	// QueryNamespace selects the namespace; required for single-object
	// requests on a cluster-wide server.
	QueryNamespace = "namespace"
	// HeaderToken carries the API token (the API server's
	// --auth-token-file). Holding it is what lets a client in.
	HeaderToken = "X-Kubetest-Token" // #nosec G101 -- a header name, not a credential
	// HeaderUser attributes a request to an end user. Only token holders
	// (Control Center) reach the API, so it is as trustworthy as they are.
	HeaderUser = "X-Kubetest-User"
	// HeaderNextCursor carries the keyset cursor of the next page of
	// finished runs on GET /runs.
	HeaderNextCursor = "X-Next-Cursor"
	// TagCreatedBy is the TestRun tag the server sets from HeaderUser.
	TagCreatedBy = "kubetest.io/created-by"
)

// Error reasons (Error.Reason).
const (
	ReasonNotFound        = "NotFound"
	ReasonBadRequest      = "BadRequest"
	ReasonConflict        = "Conflict"
	ReasonManagedByGitOps = "ManagedByGitOps"
	ReasonInternal        = "Internal"
	ReasonServiceUnavail  = "ServiceUnavailable"
	// ReasonGone: the thing existed but is over (a finished run's live view).
	ReasonGone = "Gone"
	// ReasonUnauthorized: no or a wrong API token (HeaderToken).
	ReasonUnauthorized = "Unauthorized"
)

// Error is the body of every non-2xx JSON response.
type Error struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
}

// Run is a test run — live (from the cluster) or archived (from run
// history). GET /runs and GET /runs/{id}.
type Run struct {
	UID        string     `json:"uid"`
	Name       string     `json:"name"`
	Namespace  string     `json:"namespace"`
	TestRef    string     `json:"testRef"`
	Phase      string     `json:"phase"`
	Source     string     `json:"source,omitempty"`
	QueuedAt   *time.Time `json:"queuedAt,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	Message    string     `json:"message,omitempty"`
	// Origin is "cluster" (the TestRun still exists) or "archive" (run
	// history only; can't be aborted).
	Origin string `json:"origin"`

	Tool      string            `json:"tool,omitempty"`
	ParentRun string            `json:"parentRun,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	// Config is the effective parameter set (defaults + run overrides).
	Config     map[string]string     `json:"config,omitempty"`
	TestCounts *TestCounts           `json:"testCounts,omitempty"`
	Metrics    map[string]float64    `json:"metrics,omitempty"`
	Steps      map[string]StepResult `json:"steps,omitempty"`
	// Abort is set once someone (or something) asked the run to stop.
	Abort *testsv1alpha1.AbortRequest `json:"abort,omitempty"`
	// NotBefore is when a scheduled run may start (TestRun.spec.notBefore).
	NotBefore *time.Time `json:"notBefore,omitempty"`
	// Comment is the run's note. Only finished runs (in run history) have one.
	Comment *Comment `json:"comment,omitempty"`
	// Git is the code the run checked out, for a Test with a git source.
	// Set once the run has finished.
	Git *GitCheckout `json:"git,omitempty"`
	// LiveView is the entry path of the tool's live web UI
	// (spec.liveView), for GET /runs/{id}/live/{path}; set while the run
	// is not finished.
	LiveView string `json:"liveView,omitempty"`
	// Report is the artifact path of the run's main report
	// (spec.artifacts.report), when the run produced one.
	Report string `json:"report,omitempty"`
}

// RunEvent is a Kubernetes event about one of a run's objects (its Job,
// pods, workers, services) — GET /runs/{id}/events. Kubernetes keeps events
// for about an hour.
type RunEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`   // Normal | Warning
	Reason  string    `json:"reason"` // Pulling, Scheduled, FailedScheduling, BackOff, …
	Object  string    `json:"object"` // Pod/load-1-x7k2p
	Message string    `json:"message"`
	Count   int32     `json:"count,omitempty"`
}

// GitCheckout is what a run's content fetcher checked out.
type GitCheckout struct {
	// URI is the repository (spec.content.git.uri, credentials removed).
	URI string `json:"uri,omitempty"`
	// Revision is the revision the Test asked for ("HEAD" when unset).
	Revision string `json:"revision,omitempty"`
	// Commit is the full SHA that was checked out.
	Commit string `json:"commit,omitempty"`
}

// Comment is a note on a finished run. One per run; setting it again
// replaces it.
type Comment struct {
	Text string    `json:"text"`
	By   string    `json:"by,omitempty"`
	At   time.Time `json:"at"`
}

// CommentOptions is the body of PUT /runs/{id}/comment.
type CommentOptions struct {
	Text string `json:"text"`
}

// MaxCommentLen bounds Comment.Text (characters).
const MaxCommentLen = 500

// AuditEntry is one user action recorded by the API server (GET /audit).
type AuditEntry struct {
	ID        int64             `json:"id"`
	At        time.Time         `json:"at"`
	Actor     string            `json:"actor,omitempty"`
	Action    string            `json:"action"`
	Namespace string            `json:"namespace,omitempty"`
	Target    string            `json:"target,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// TestCase is one JUnit test case of a finished run
// (GET /runs/{id}/testcases).
type TestCase struct {
	// Key identifies the case across runs: class (or suite) › name.
	Key        string `json:"key"`
	Suite      string `json:"suite,omitempty"`
	Class      string `json:"class,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"` // passed | failed | error | skipped
	DurationMs int64  `json:"durationMs,omitempty"`
	Message    string `json:"message,omitempty"`
	Details    string `json:"details,omitempty"`
	File       string `json:"file,omitempty"`
}

// CaseStats aggregates one test case over a Test's recent runs
// (GET /tests/{name}/testcases).
type CaseStats struct {
	Key   string `json:"key"`
	Suite string `json:"suite,omitempty"`
	Class string `json:"class,omitempty"`
	Name  string `json:"name"`
	// Runs is how many of the window's runs had the case.
	Runs       int       `json:"runs"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"` // failed + errored
	Skipped    int       `json:"skipped"`
	AvgMs      int64     `json:"avgMs"`
	MaxMs      int64     `json:"maxMs"`
	LastStatus string    `json:"lastStatus"`
	LastAt     time.Time `json:"lastAt"`
	// Flips counts passed ↔ not-passed changes between consecutive runs.
	Flips int `json:"flips"`
	// Flaky: the case both passed and failed within the window.
	Flaky bool `json:"flaky"`
}

// CaseRun is one run's result for one test case
// (GET /tests/{name}/testcases/history).
type CaseRun struct {
	RunUID     string    `json:"runUid"`
	RunName    string    `json:"runName,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
	Status     string    `json:"status"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// Audit actions.
const (
	ActionRunCreate    = "run.create"
	ActionRunAbort     = "run.abort"
	ActionRunDelete    = "run.delete"
	ActionRunComment   = "run.comment"
	ActionRunUncomment = "run.uncomment"
	ActionTestCreate   = "test.create"
	ActionTestUpdate   = "test.update"
	ActionTestDelete   = "test.delete"
)

// AuditEntry.Details keys.
const (
	AuditDetailUID       = "uid"
	AuditDetailTest      = "test"
	AuditDetailMessage   = "message"
	AuditDetailText      = "text"
	AuditDetailNotBefore = "notBefore"
)

// Origins of a Run.
const (
	OriginCluster = "cluster"
	OriginArchive = "archive"
)

// TestCounts are JUnit-derived test counts.
type TestCounts struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// StepResult is one composite step (or child) of a Run.
type StepResult struct {
	Phase      string     `json:"phase,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Message    string     `json:"message,omitempty"`
}

// Artifact is one entry of GET /runs/{id}/artifacts.
type Artifact struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// ArtifactURL is GET /runs/{id}/artifacts/{path}?presign=1.
type ArtifactURL struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expiresIn"`
}

// ResolvedTest is GET /tests/{name}/resolved: the Test merged with its
// templates, for building run forms.
type ResolvedTest struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
	// Tool is the Test's kubetest.io/tool label, else its templates'.
	Tool string `json:"tool,omitempty"`
	// GitOpsLocked: the definition is read-only in the GUI (§7); runs are
	// still allowed.
	GitOpsLocked bool `json:"gitopsLocked"`
	// Templates is spec.use, in merge order.
	Templates []string `json:"templates,omitempty"`
	// Spec: expressions not evaluated, config not resolved — spec.config
	// is the full parameter schema.
	Spec *testsv1alpha1.TestSpec `json:"spec"`
}

// AbortOptions is the optional body of POST /runs/{id}/abort.
type AbortOptions struct {
	Message string `json:"message,omitempty"`
}
