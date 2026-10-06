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
	// HeaderUser attributes a request to an end user (set by a trusted
	// front end such as Control Center). Attribution, not authentication.
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
}

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
