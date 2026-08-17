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

// Package compiler turns a (Test, TestRun) pair into a batchv1.Job plus its
// auxiliary Kubernetes objects. The function is pure: no client calls, no
// clocks, no I/O. All inputs come in via parameters; outputs go out as return
// values. This is the highest-leverage unit-test target in the repository —
// see step-03 acceptance in plan/step-03-compiler.md.
package compiler

import (
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// Filesystem contract with the /entry wrapper (step 05). These paths are the
// operator side of a shared agreement — the wrapper must read/write them.
const (
	// DataDirPath is the shared emptyDir mounted into both the content-fetcher
	// init container and the main wrapper container. Content lands here.
	DataDirPath = "/data"

	// RequestMountDir is where the ExecutionRequest ConfigMap is projected.
	// The wrapper reads RequestFileName from this directory.
	RequestMountDir = "/etc/kubetest"

	// RequestFileName is the ConfigMap key + projected filename for the
	// wrapper request. Aliased from pkg/executor so both sides stay in sync.
	RequestFileName = executor.RequestFileName

	// ResultDirPath is where the wrapper writes result.json. The operator
	// (step 04+) reads this via a post-scrape or from the pod's terminated
	// container status.
	ResultDirPath = "/etc/kubetest/result"

	// EnvContentJSON is the init container's env var carrying Content spec
	// as JSON. Step 06 replaces this with a ConfigMap-mounted file (see
	// plan/step-06 NOTE) — inline files near the 512KB webhook cap can't ride
	// safely on env.
	EnvContentJSON = "KUBETEST_CONTENT_JSON"

	// Volume names on the pod template. Fixed strings so golden fixtures
	// remain stable when new fields are added.
	VolumeData    = "data"
	VolumeRequest = "request"
	VolumeResult  = "result"
	VolumeDevShm  = "dev-shm"

	// Container names.
	ContainerContentFetcher = "content-fetcher"
	ContainerWrapper        = "wrapper"

	// kindTestRun is used in ownerRef and TypeMeta on aux objects. Extracting
	// avoids duplicating a literal Kind string in three places.
	kindTestRun = "TestRun"

	// DefaultTimeout is applied when Test.spec.timeout is unset. Long enough
	// for a substantial cypress suite; short enough that a stuck test doesn't
	// squat on a Job for hours before ADS fires.
	DefaultTimeout = 30 * time.Minute

	// ADSBufferSeconds is added to the wrapper timeout to compute the Job's
	// activeDeadlineSeconds. The wrapper handles its own SIGTERM within this
	// buffer to flush partial results + artifacts before the Job's outer
	// SIGKILL. See CLAUDE.md §15.3.
	ADSBufferSeconds = 60

	// TTLSecondsAfterFinished is the safety-net kubelet-side TTL on finished
	// Jobs (§15.3). The reconciler deletes Jobs explicitly after persisting
	// status, so this only kicks in on paths where the controller doesn't:
	// TestRun force-deleted with the finalizer manually stripped, a bug in
	// the reconcile loop, or the operator being uninstalled from the
	// namespace. Without it those Jobs sit in etcd until human cleanup.
	//
	// 1 hour is long enough to debug a stuck run before the Job vanishes,
	// short enough that a hung namespace doesn't drown in stale Jobs.
	TTLSecondsAfterFinished int32 = 3600
)

// Options configures Compile. All fields are optional except ContentFetcherImage.
type Options struct {
	// ContentFetcherImage names the init container that materializes
	// Test.spec.content into /data. Step 06 fills in the actual fetching
	// binary; step 03 requires the reference to be set so the pod is valid.
	ContentFetcherImage string

	// ImageRegistry, if set, is prepended to every executor image reference
	// (e.g. "mycorp.registry/mirror" -> "mycorp.registry/mirror/grafana/k6:2.2.0").
	// Intended for private-registry mirrors of upstream images. Per-Test
	// container.image overrides bypass this prefix (treated as fully qualified).
	ImageRegistry string

	// ExecutorImages overrides DefaultExecutorImages entries per type.
	// Missing keys fall back to defaults. Applied BEFORE ImageRegistry prefixing.
	// This is the hook for a future operator config (Helm value, CRD field).
	ExecutorImages map[string]string
}

// Note: the ExecutionRequest wire format lives in pkg/executor as the public
// contract between the operator (which serializes) and the /entry wrapper
// (which deserializes). Secret-typed params planned for a later step MUST
// NOT be added there — they will arrive via envFrom+Secret projection so the
// ConfigMap remains safe to store unencrypted in etcd.

// Compile turns (test, run) into a Job plus auxiliary objects.
//
// Return contract:
//   - job: the batchv1.Job the caller must Create in the cluster.
//   - aux: extra objects that must be Create'd BEFORE the Job (currently the
//     ExecutionRequest ConfigMap). ownerRefs are set so cascade delete via
//     TestRun cleans everything up (§15.5).
//   - err: typed via errors.Is (ErrNilTest, ErrNilTestRun, ErrUnknownExecutor,
//     ErrMissingContentFetcherImage).
//
// Never mutates the input objects.
func Compile(test *testsv1alpha1.Test, run *testsv1alpha1.TestRun, opts Options) (*batchv1.Job, []client.Object, error) {
	if test == nil {
		return nil, nil, ErrNilTest
	}
	if run == nil {
		return nil, nil, ErrNilTestRun
	}
	if opts.ContentFetcherImage == "" {
		return nil, nil, ErrMissingContentFetcherImage
	}

	image, command, args, ok := resolveExecutor(
		test.Spec.Type, opts,
		test.Spec.Container.Image, test.Spec.Container.Command, test.Spec.Container.Args,
	)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrUnknownExecutor, test.Spec.Type)
	}

	timeout := DefaultTimeout
	if test.Spec.Timeout != nil {
		timeout = test.Spec.Timeout.Duration
	}
	timeoutSec := int64(timeout.Seconds())
	adsSec := timeoutSec + ADSBufferSeconds

	requestJSON, err := buildRequestJSON(test, run, image, command, args, timeoutSec)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal execution request: %w", err)
	}

	// Content spec ships as a ConfigMap key alongside request.json (see step
	// 03 NOTE + step 06). The env-var approach broke on inline files near
	// 512KB — pod object bloat in etcd, ARG_MAX risk, leaks in kubectl describe.
	contentJSON, err := json.Marshal(test.Spec.Content)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal content spec: %w", err)
	}

	cmName := run.Name + "-request"
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            cmName,
			Namespace:       run.Namespace,
			OwnerReferences: ownerRefForTestRun(run),
			Labels: map[string]string{
				LabelRunID:     run.Name,
				LabelManagedBy: ManagedByValue,
			},
		},
		Data: map[string]string{
			RequestFileName:          requestJSON,
			executor.ContentFileName: string(contentJSON),
		},
	}

	mergedPod := mergePodConfig(test.Spec.Pod, run.Spec.Pod)
	podLabels := mergeLabels(test.Spec.Pod, run.Spec.Pod, run.Name)
	podAnnotations := mergeAnnotations(test.Spec.Pod, run.Spec.Pod)

	volumes := []corev1.Volume{
		{Name: VolumeData, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: VolumeResult, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: VolumeRequest, VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				// Explicit Items so any future stray CM key isn't projected
				// into the pod by accident.
				Items: []corev1.KeyToPath{
					{Key: RequestFileName, Path: RequestFileName},
					{Key: executor.ContentFileName, Path: executor.ContentFileName},
				},
			},
		}},
	}
	volumes = append(volumes, mergedPod.Volumes...)

	// Init container reads content.json from the same ConfigMap the wrapper
	// uses for request.json — one CM, two files, one mount.
	// Command = ["/entry", "fetch"] — same binary as the wrapper, different
	// subcommand (see cmd/entry/main.go dispatch).
	initContainers := []corev1.Container{{
		Name:    ContainerContentFetcher,
		Image:   opts.ContentFetcherImage,
		Command: []string{executor.EntryCommand, "fetch"},
		Env: []corev1.EnvVar{
			{Name: executor.EnvDataDir, Value: DataDirPath},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: VolumeData, MountPath: DataDirPath},
			{Name: VolumeRequest, MountPath: RequestMountDir, ReadOnly: true},
		},
	}}

	wrapperEnv := make([]corev1.EnvVar, 0, 4+len(test.Spec.Container.Env))
	wrapperEnv = append(wrapperEnv,
		corev1.EnvVar{Name: executor.EnvDataDir, Value: DataDirPath},
		corev1.EnvVar{Name: executor.EnvResultDir, Value: ResultDirPath},
		corev1.EnvVar{Name: executor.EnvRunID, Value: run.Name},
		corev1.EnvVar{Name: executor.EnvTestRef, Value: run.Spec.TestRef},
	)
	wrapperEnv = append(wrapperEnv, test.Spec.Container.Env...)

	wrapperMounts := []corev1.VolumeMount{
		{Name: VolumeData, MountPath: DataDirPath},
		{Name: VolumeRequest, MountPath: RequestMountDir, ReadOnly: true},
		{Name: VolumeResult, MountPath: ResultDirPath},
	}
	wrapperMounts = append(wrapperMounts, test.Spec.Container.VolumeMounts...)

	// Cypress-specific fix from CLAUDE.md §15.3: Chrome crashes without a
	// large /dev/shm; use memory-backed emptyDir. Applied only for the cypress
	// executor to avoid pointlessly reserving RAM for other tools.
	if test.Spec.Type == ExecutorCypress {
		medium := corev1.StorageMediumMemory
		volumes = append(volumes, corev1.Volume{
			Name: VolumeDevShm,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium},
			},
		})
		wrapperMounts = append(wrapperMounts, corev1.VolumeMount{
			Name: VolumeDevShm, MountPath: "/dev/shm",
		})
	}

	// Container.Command is ALWAYS /entry: the pod runs the wrapper, and the
	// wrapper reads request.json to figure out what tool to invoke.
	// `command` from resolveExecutor is the TOOL command override (usually
	// nil / user-provided via Test.spec.container.command) — it lands in
	// request.json.Command for the Runner to consume, not on the pod.
	wrapper := corev1.Container{
		Name:            ContainerWrapper,
		Image:           image,
		Command:         []string{executor.EntryCommand},
		Args:            args,
		Env:             wrapperEnv,
		EnvFrom:         test.Spec.Container.EnvFrom,
		WorkingDir:      test.Spec.Container.WorkingDir,
		ImagePullPolicy: test.Spec.Container.ImagePullPolicy,
		Resources:       test.Spec.Container.Resources,
		SecurityContext: test.Spec.Container.SecurityContext,
		VolumeMounts:    wrapperMounts,
	}

	automountSAToken := false
	backoffLimit := int32(0)
	ttl := TTLSecondsAfterFinished

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            run.Name,
			Namespace:       run.Namespace,
			OwnerReferences: ownerRefForTestRun(run),
			Labels: map[string]string{
				LabelRunID:     run.Name,
				LabelManagedBy: ManagedByValue,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &adsSec,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automountSAToken,
					ServiceAccountName:           mergedPod.ServiceAccountName,
					NodeSelector:                 mergedPod.NodeSelector,
					Tolerations:                  mergedPod.Tolerations,
					Affinity:                     mergedPod.Affinity,
					SecurityContext:              mergedPod.SecurityContext,
					ImagePullSecrets:             mergedPod.ImagePullSecrets,
					InitContainers:               initContainers,
					Containers:                   []corev1.Container{wrapper},
					Volumes:                      volumes,
				},
			},
		},
	}

	return job, []client.Object{cm}, nil
}

// ownerRefForTestRun builds the single ownerRef pointing at the TestRun.
// controller=true so k8s treats TestRun as the owning controller; when TestRun
// is deleted, the child (Job or ConfigMap) is garbage-collected.
//
// NOTE: We do NOT set an ownerRef Test→TestRun anywhere — §15.5 forbids it so
// deleting a Test definition doesn't wipe run history.
func ownerRefForTestRun(run *testsv1alpha1.TestRun) []metav1.OwnerReference {
	trueVal := true
	return []metav1.OwnerReference{{
		APIVersion:         testsv1alpha1.SchemeGroupVersion.String(),
		Kind:               kindTestRun,
		Name:               run.Name,
		UID:                run.UID,
		Controller:         &trueVal,
		BlockOwnerDeletion: &trueVal,
	}}
}

// buildRequestJSON marshals the wire payload the wrapper reads from
// $REQUEST_MOUNT_DIR/request.json. Kept separate so tests can exercise it
// without going through the whole Compile path.
//
// `command` here is the TOOL command (user override via Test.spec.container.command
// or nil for defaults). NOT to be confused with container.Command on the pod,
// which is always /entry.
func buildRequestJSON(test *testsv1alpha1.Test, run *testsv1alpha1.TestRun,
	image string, command, args []string, timeoutSec int64) (string, error) {
	req := executor.ExecutionRequest{
		// Type feeds step-11's single-/entry dispatch: the wrapper's Runner
		// registry looks itself up by this string.
		Type:           test.Spec.Type,
		RunID:          run.Name,
		TestRef:        run.Spec.TestRef,
		DataDir:        DataDirPath,
		WorkingDir:     test.Spec.Container.WorkingDir,
		Command:        command,
		Args:           args,
		Env:            envSliceToMap(test.Spec.Container.Env),
		Config:         run.Spec.Config,
		TimeoutSeconds: timeoutSec,
	}
	if test.Spec.Artifacts != nil {
		req.Artifacts = executor.ArtifactSpec{
			Paths:    append([]string(nil), test.Spec.Artifacts.Paths...),
			Compress: test.Spec.Artifacts.Compress,
		}
	}
	// image is captured in the ConfigMap primarily for debugging — the pod
	// already carries the effective image on containers[0].image.
	_ = image
	b, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func envSliceToMap(env []corev1.EnvVar) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, e := range env {
		// Only carry literal values; ValueFrom entries stay on the pod itself
		// (they need k8s to resolve refs — the wrapper doesn't have API access).
		if e.ValueFrom == nil {
			out[e.Name] = e.Value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
