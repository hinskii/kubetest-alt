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

package apiserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func TestCreateTest_DryRunDoesNotPersist(t *testing.T) {
	s, h := mkServer(t)
	body := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "wiz", Namespace: "default"},
		Spec:       testsv1alpha1.TestSpec{Container: testsv1alpha1.ContainerConfig{Image: "busybox", Args: []string{"true"}}},
	}
	rec, out := doRequest(t, h, "POST", "/tests?dry-run=true", body)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "wiz", out["metadata"].(map[string]any)["name"])
	err := s.K8sClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "wiz"}, &testsv1alpha1.Test{})
	assert.True(t, apierrors.IsNotFound(err), "a dry run creates nothing")

	rec, _ = doRequest(t, h, "POST", "/tests", body)
	require.Equal(t, http.StatusCreated, rec.Code)
}

func TestPatchTest_DryRunDoesNotPersist(t *testing.T) {
	s, h := mkServer(t, mkTest("ui-owned", ManagedByUI))
	rec, _ := doRequest(t, h, "PATCH", "/tests/ui-owned?dry-run=true",
		map[string]any{"spec": map[string]any{"schedule": "0 1 * * *"}})
	require.Equal(t, http.StatusOK, rec.Code)
	var got testsv1alpha1.Test
	require.NoError(t, s.K8sClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ui-owned"}, &got))
	assert.Empty(t, got.Spec.Schedule)
}

func TestListTemplates(t *testing.T) {
	tmpl := func(name, ns string) *testsv1alpha1.TestTemplate {
		return &testsv1alpha1.TestTemplate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	}
	_, h := mkServer(t, tmpl("k6", "default"), tmpl("cypress", "default"), tmpl("k6", "other"))
	rec, _ := doRequest(t, h, "GET", "/templates", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"cypress"`)
	assert.NotContains(t, rec.Body.String(), `"other"`, "the server's namespace only")
}
