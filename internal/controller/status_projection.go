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

package controller

import (
	"strconv"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// applyRunResultToStatus folds the wrapper's RunResult into the TestRun status
// object AT THE MEMORY LEVEL (in-place mutation, no k8s API call). The
// caller's subsequent status Update lands everything atomically.
//
// Fields projected:
//   - Metrics — float→string encoded so the CRD schema stays simple
//     (map[string]string is trivially valid in k8s API).
//   - TestCounts — copied verbatim.
//   - ArtifactRefs — copied verbatim (ArtifactRef shape aligned across
//     pkg/executor and api/v1alpha1 in step 07).
//
// ScrapeError is NOT projected to status yet — the plan reserves status
// space for it in a later step. Wrapper still writes it into result.json
// for post-mortem visibility.
func applyRunResultToStatus(run *testsv1alpha1.TestRun, r *RunResult) {
	if r == nil {
		return
	}
	if len(r.Metrics) > 0 {
		out := make(map[string]string, len(r.Metrics))
		for k, v := range r.Metrics {
			out[k] = strconv.FormatFloat(v, 'f', -1, 64)
		}
		run.Status.Metrics = out
	}
	if r.TestCounts != nil {
		run.Status.TestCounts = r.TestCounts
	}
	if len(r.Artifacts) > 0 {
		run.Status.ArtifactRefs = r.Artifacts
	}
}
