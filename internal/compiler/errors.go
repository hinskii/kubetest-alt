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

package compiler

import "errors"

// Typed sentinel errors so callers can react programmatically
// (`errors.Is(err, compiler.ErrUnknownExecutor)`).
var (
	// ErrNilTest indicates Compile was called with a nil *Test pointer.
	ErrNilTest = errors.New("compiler: test is nil")

	// ErrNilTestRun indicates Compile was called with a nil *TestRun pointer.
	ErrNilTestRun = errors.New("compiler: testrun is nil")

	// ErrUnknownExecutor indicates spec.type isn't in DefaultExecutorImages.
	// (The Test webhook rejects unknown types at admission, but the compiler
	// still validates as a defense-in-depth measure for tests that skip the
	// webhook path.)
	ErrUnknownExecutor = errors.New("compiler: unknown executor type")

	// ErrMissingContentFetcherImage indicates Options.ContentFetcherImage
	// wasn't provided; the operator must supply it (step 06 fills this in).
	ErrMissingContentFetcherImage = errors.New("compiler: Options.ContentFetcherImage is required")
)
