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
	"errors"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/resolver"
)

// EnvTestDataDir tells the test where testData entries without a
// mountPath or items are: $KUBETEST_TESTDATA_DIR/<name>/<key>.
const EnvTestDataDir = "KUBETEST_TESTDATA_DIR"

// ErrContentFrom: files[].contentFrom never delivered a file; data from
// the cluster is content.testData (step 20i).
var ErrContentFrom = errors.New("content.files[].contentFrom isn't supported — mount the ConfigMap or Secret with content.testData")

// fetcherContent is what the content fetcher materialises (the JSON shape
// of pkg/executor/fetcher.Content): inline files with their paths below
// /data (the CRD's are below /data/repo), and the testData mount points it
// must find empty after the checkout.
type fetcherContent struct {
	Git         *testsv1alpha1.GitContent `json:"git,omitempty"`
	Files       []fetcherFile             `json:"files,omitempty"`
	Tarball     []testsv1alpha1.Tarball   `json:"tarball,omitempty"`
	EmptyMounts []emptyMount              `json:"emptyMounts,omitempty"`
}

type fetcherFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Mode    *int32 `json:"mode,omitempty"`
}

// emptyMount: a testData mountPath below /data that must not hide files
// of the code.
type emptyMount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// repoRelDir is RepoDir relative to the data dir: where inline files go.
var repoRelDir = strings.TrimPrefix(resolver.RepoDir, DataDirPath+"/")

// toFetcherContent maps a Test's content to the fetcher's.
func toFetcherContent(c testsv1alpha1.Content) (fetcherContent, error) {
	out := fetcherContent{Git: c.Git, Tarball: c.Tarball}
	for _, f := range c.Files {
		if f.ContentFrom != nil {
			return out, ErrContentFrom
		}
		out.Files = append(out.Files, fetcherFile{
			Path:    path.Join(repoRelDir, resolver.InlineFilePath(f.Path)),
			Content: f.Content, Mode: f.Mode,
		})
	}
	for _, d := range c.TestData {
		if d.MountPath != "" && strings.HasPrefix(path.Clean(d.MountPath)+"/", DataDirPath+"/") {
			out.EmptyMounts = append(out.EmptyMounts, emptyMount{Name: testDataName(d), Path: path.Clean(d.MountPath)})
		}
	}
	return out, nil
}

func testDataName(d testsv1alpha1.TestDataSource) string {
	if d.Secret != "" {
		return d.Secret
	}
	return d.ConfigMap
}

// testDataVolumes mounts content.testData read-only in the test
// container: every key at /data/testdata/<name>/<key> by default, in
// mountPath, or the chosen keys at exact paths (subPath mounts).
func testDataVolumes(entries []testsv1alpha1.TestDataSource) ([]corev1.Volume, []corev1.VolumeMount, error) {
	var vols []corev1.Volume
	var mounts []corev1.VolumeMount
	for i, d := range entries {
		name := fmt.Sprintf("testdata-%d", i)
		var src corev1.VolumeSource
		switch {
		case d.ConfigMap != "" && d.Secret == "":
			src.ConfigMap = &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: d.ConfigMap}}
		case d.Secret != "" && d.ConfigMap == "":
			src.Secret = &corev1.SecretVolumeSource{SecretName: d.Secret}
		default:
			return nil, nil, fmt.Errorf("content.testData[%d]: set exactly one of configMap or secret", i)
		}
		vols = append(vols, corev1.Volume{Name: name, VolumeSource: src})
		switch {
		case len(d.Items) > 0:
			for _, it := range d.Items {
				mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: it.Path, SubPath: it.Key, ReadOnly: true})
			}
		case d.MountPath != "":
			mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: d.MountPath, ReadOnly: true})
		default:
			mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: path.Join(resolver.TestDataDir, testDataName(d)), ReadOnly: true})
		}
	}
	return vols, mounts, nil
}
