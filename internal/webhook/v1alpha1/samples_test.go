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

package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// TestAcceptance_SamplesApply is the step-02 acceptance gate:
// `kubectl apply` of the four sample manifests in config/samples/ must
// succeed against envtest with webhooks live. We use unstructured so this
// test would catch a sample that references a field the typed API doesn't
// carry (which a typed decoder would silently drop).
func TestAcceptance_SamplesApply(t *testing.T) {
	root, err := findRepoRoot()
	require.NoError(t, err)

	samples := []string{
		"tests_v1alpha1_test.yaml",
		"tests_v1alpha1_testrun.yaml",
		"tests_v1alpha1_testtemplate.yaml",
		"tests_v1alpha1_testtrigger.yaml",
	}

	ctx := context.Background()
	for _, name := range samples {
		t.Run(name, func(t *testing.T) {
			// #nosec G304 -- path is joined from findRepoRoot() + a fixed sample filename table above.
			raw, err := os.ReadFile(filepath.Join(root, "config", "samples", name))
			require.NoError(t, err)

			obj := &unstructured.Unstructured{}
			require.NoError(t, yaml.Unmarshal(raw, obj))
			// Give it a namespace since the samples don't set one and none of
			// our CRDs are cluster-scoped.
			if obj.GetNamespace() == "" {
				obj.SetNamespace("default")
			}
			require.NoError(t, k8sClient.Create(ctx, obj), "sample %s must apply cleanly", name)
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		})
	}
}
