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

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveExecutor_DefaultsForKnownTypes(t *testing.T) {
	for _, execType := range []string{"k6", "cypress", "newman", "locust", "jmeter"} {
		t.Run(execType, func(t *testing.T) {
			image, _, args, ok := resolveExecutor(execType, Options{}, "", nil, nil)
			require.True(t, ok)
			assert.NotEmpty(t, image, "image must be pinned for %s", execType)
			assert.NotEmpty(t, args, "args must be pre-populated for %s", execType)
		})
	}
}

func TestResolveExecutor_UnknownReturnsFalse(t *testing.T) {
	_, _, _, ok := resolveExecutor("artillery", Options{}, "", nil, nil)
	assert.False(t, ok)
}

func TestResolveExecutor_OptionsOverrideAppliedBeforeRegistryPrefix(t *testing.T) {
	opts := Options{
		ExecutorImages: map[string]string{"k6": "custom/k6:1.0"},
		ImageRegistry:  "mirror.internal",
	}
	image, _, _, ok := resolveExecutor("k6", opts, "", nil, nil)
	require.True(t, ok)
	assert.Equal(t, "mirror.internal/custom/k6:1.0", image,
		"registry prefix must apply on top of ExecutorImages override")
}

func TestResolveExecutor_PerTestImageBypassesRegistryPrefix(t *testing.T) {
	// A per-Test image is treated as fully qualified — user's problem to include
	// their own registry prefix if they need one. This avoids double-prefix
	// bugs like `mirror.internal/my.private.registry/tool:v1`.
	opts := Options{ImageRegistry: "mirror.internal"}
	image, _, _, ok := resolveExecutor("k6", opts, "user.registry/team/k6:x", nil, nil)
	require.True(t, ok)
	assert.Equal(t, "user.registry/team/k6:x", image)
}

func TestResolveExecutor_PerTestCommandAndArgsOverride(t *testing.T) {
	image, command, args, ok := resolveExecutor("k6", Options{}, "",
		[]string{"/custom/entry"}, []string{"one", "two"})
	require.True(t, ok)
	assert.Equal(t, "grafana/k6:2.2.0", image, "image untouched when only cmd/args override")
	assert.Equal(t, []string{"/custom/entry"}, command)
	assert.Equal(t, []string{"one", "two"}, args)
}

func TestResolveExecutor_EmptyOptionsExecutorImagesEntryIgnored(t *testing.T) {
	// A user setting ExecutorImages["k6"] = "" must NOT wipe the default.
	// Guards against a common config-loading pitfall (unset YAML field
	// deserializes as "").
	opts := Options{ExecutorImages: map[string]string{"k6": ""}}
	image, _, _, ok := resolveExecutor("k6", opts, "", nil, nil)
	require.True(t, ok)
	assert.Equal(t, "grafana/k6:2.2.0", image)
}

func TestResolveExecutor_ArgsAndCommandAreCopies(t *testing.T) {
	// Guard against the compiler leaking a reference to the shared
	// DefaultExecutorImages slice — a mutation downstream would poison every
	// future compilation. Regression: slice-append-in-place surprises.
	_, _, args1, _ := resolveExecutor("k6", Options{}, "", nil, nil)
	_, _, args2, _ := resolveExecutor("k6", Options{}, "", nil, nil)
	require.NotEmpty(t, args1)
	args1[0] = "MUTATED"
	assert.NotEqual(t, "MUTATED", args2[0], "returned args must be independent copies")
	// And DefaultExecutorImages itself is untouched.
	assert.NotEqual(t, "MUTATED", DefaultExecutorImages["k6"].Args[0])
}
