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

package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// TestE2E_512KBInline_ThroughCompilerAndFetcher is the step-03 NOTE / step-06
// end-to-end acceptance: inline files summing near the 512KB webhook cap must
// flow from Test.spec → compiled ConfigMap → fetcher → files on disk in /data,
// with the same bytes on both ends.
//
// Two total files at 250KB each = 500KB aggregate. Under the webhook cap
// (512KB), representative of a realistic inline test payload.
//
// This is the test the step-03 NOTE explicitly asked for.
func TestE2E_512KBInline_ThroughCompilerAndFetcher(t *testing.T) {
	const chunk = 250 * 1024
	payloadA := strings.Repeat("A", chunk)
	payloadB := strings.Repeat("B", chunk)

	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "big-inline", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Type: "k6",
			Content: testsv1alpha1.Content{
				Files: []testsv1alpha1.FileContent{
					{Path: "big-a.txt", Content: payloadA},
					{Path: "sub/big-b.txt", Content: payloadB},
				},
			},
		},
	}
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "big-run", Namespace: "ns"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "big-inline"},
	}

	// Step 1: compile — content lands in the ConfigMap under content.json,
	// NOT on the init container's env (regression guard for step-03 NOTE).
	_, aux, err := compiler.Compile(test, run, compiler.Options{
		ContentFetcherImage: "test/content-fetcher:v0",
	})
	require.NoError(t, err)
	require.Len(t, aux, 1)
	cm := aux[0].(*corev1.ConfigMap)
	require.Contains(t, cm.Data, executor.ContentFileName)

	// Step 2: parse the content.json back out (mirrors what the fetcher does
	// in the init container).
	var got Content
	require.NoError(t, json.Unmarshal([]byte(cm.Data[executor.ContentFileName]), &got))
	require.Len(t, got.Files, 2, "both inline files must round-trip through JSON")

	// Step 3: run the fetcher against a temp datadir, feeding it the same
	// content.json the init container would see mounted.
	dst := t.TempDir()
	f := NewFetcher()
	f.Stdout = &bytes.Buffer{}
	f.Stderr = &bytes.Buffer{}
	require.NoError(t, f.Fetch(context.Background(), got, dst))

	// Step 4: assert both files landed with the EXACT bytes we started with.
	// #nosec G304 -- test path.
	fileA, err := os.ReadFile(filepath.Join(dst, "big-a.txt"))
	require.NoError(t, err)
	assert.Equal(t, chunk, len(fileA), "big-a.txt size mismatch")
	assert.Equal(t, payloadA, string(fileA))

	// #nosec G304 -- test path.
	fileB, err := os.ReadFile(filepath.Join(dst, "sub/big-b.txt"))
	require.NoError(t, err)
	assert.Equal(t, chunk, len(fileB), "sub/big-b.txt size mismatch")
	assert.Equal(t, payloadB, string(fileB))
}
