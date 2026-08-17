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

package executor

import "context"

// Runner runs one instance of a testing tool inside the /entry wrapper.
//
// Contract:
//   - Run MUST NOT return a non-nil error for test outcomes. The verdict
//     (passed/failed/error/aborted) lives in the returned ExecutionResult.Phase;
//     returning an error is reserved for INFRA-level failures the wrapper
//     cannot classify (e.g. tool binary missing entirely). Everything else —
//     non-zero exit codes, timeouts, threshold violations — belongs in the
//     result.
//   - Run MUST stream tool stdout to the writer the Runner was constructed
//     with (line-buffered so the operator's log tail sees real-time output).
//   - Run MUST honor ctx cancellation: on ctx.Done the tool process must be
//     killed. exec.CommandContext handles this for straightforward cases.
type Runner interface {
	// Type identifies the executor kind (e.g. "k6"). Used only for logging
	// and diagnostics — dispatch decisions live in the wrapper.
	Type() string

	// Validate is a pre-flight check on the request. Returns nil if the
	// request is well-formed for this Runner. Non-nil error means the
	// wrapper aborts before invoking the tool.
	Validate(ctx context.Context, req ExecutionRequest) error

	// Run executes the tool and returns the verdict.
	Run(ctx context.Context, req ExecutionRequest) (ExecutionResult, error)
}
