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

// managed_by.go implements the CLAUDE.md §7 policy: GUI-driven CRUD is
// blocked on Tests owned by GitOps (managed-by: gitops), but starting a
// TestRun against a GitOps Test is always allowed (runs are ephemeral
// children, not definitions).
//
// The label key follows the k8s convention app.kubernetes.io/managed-by.
package apiserver

// LabelManagedBy is the label the API server writes on Tests created via
// POST /tests and inspects on Tests targeted by PATCH/DELETE. Value "ui"
// on GUI-created objects; "gitops" (or any other value) marks it as
// externally-owned and blocks mutating operations.
//
// Exported so tests, admission webhooks (future), and cmd/apiserver
// integration test fixtures use the same string.
const LabelManagedBy = "app.kubernetes.io/managed-by"

// ManagedByUI is the value the API server writes on POST /tests. Kept as
// a constant so tests assert without string drift.
const ManagedByUI = "ui"

// ManagedByGitOps is the value ArgoCD (or similar) is expected to set on
// GitOps-owned Tests. Anything OTHER than "ui" is treated as
// externally-owned — plan §7 explicitly allows this "missing label →
// treat as ui" contract, and ANYTHING-ELSE is blocked.
const ManagedByGitOps = "gitops"

// isManagedByGitOps returns true when the object's managed-by label is
// set AND not equal to "ui". Missing label → false (treat as ui, per §7).
// This is the sole rule the enforcement path depends on.
func isManagedByGitOps(labels map[string]string) bool {
	v, ok := labels[LabelManagedBy]
	if !ok {
		return false
	}
	return v != ManagedByUI
}
