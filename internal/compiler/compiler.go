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

	// VolumeKubetestBin holds the /entry binary. The content-fetcher init
	// container copies /entry from its own image into this shared emptyDir;
	// the main container mounts it and runs /kubetest-bin/entry. This is
	// how the workflows-model wrapper stays out of the user's tool image —
	// the tool image can be anything (alpine, distroless, cypress/included)
	// and /entry lands via the shared volume, not the image.
	VolumeKubetestBin = "kubetest-bin"

	// KubetestBinMountPath is where the shared emptyDir mounts inside both
	// the init container (source) and the main container (destination).
	KubetestBinMountPath = "/kubetest-bin"

	// KubetestBinEntry is the resolved absolute path of the /entry binary
	// inside the main container. Cross-package const so tests and the
	// wrapper agree without extra math.
	KubetestBinEntry = KubetestBinMountPath + "/entry"

	// ContentFetcherEntrySrc is where /entry lives inside the content-fetcher
	// image (built by executors/content-fetcher/Dockerfile). The init
	// container copies from here to KubetestBinMountPath.
	ContentFetcherEntrySrc = "/entry"

	// Container names.
	ContainerContentFetcher     = "content-fetcher"
	ContainerWrapper            = "wrapper"
	ContainerKubetestBinInstall = "kubetest-bin-install"

	// LabelKubetestTool is the label templates + users set on Tests to
	// name the tool (k6, cypress, jmeter, ...). Empty when not set —
	// tools aren't part of the schema in the workflows model. The
	// compiler propagates this label from Test → Job → Pod so kubectl
	// selectors + printcolumns can group by tool.
	LabelKubetestTool = "kubetest.io/tool"

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

	// TerminationGracePeriodMarginSeconds is added to executor.ScrapeGracePeriodSeconds
	// to compute the pod's terminationGracePeriodSeconds. k8s default is 30s,
	// which is EXACTLY the scrape budget — the pod gets SIGKILL'd mid-scrape
	// under load. Margin covers the WriteResultAtomic + UploadResult steps
	// that run AFTER the scrape budget's timer starts.
	TerminationGracePeriodMarginSeconds int32 = 15
)

// terminationGracePeriodSeconds derives the pod's TGPS from the shared
// scrape budget in pkg/executor. Any future change to ScrapeGracePeriodSeconds
// propagates here without a compiler-side edit.
func terminationGracePeriodSeconds() int64 {
	return int64(executor.ScrapeGracePeriodSeconds) + int64(TerminationGracePeriodMarginSeconds)
}

// ptrInt64 keeps the pod spec construction terse — k8s API types take
// *int64 for TerminationGracePeriodSeconds and friends.
func ptrInt64(v int64) *int64 { return &v }

// Options configures Compile. All fields are optional except ContentFetcherImage.
type Options struct {
	// ContentFetcherImage names the init container that materializes
	// Test.spec.content into /data AND supplies the /entry binary that
	// the main container invokes. Required — the workflows model relies
	// on this image to carry /entry (the main image can be anything and
	// need not know /entry exists).
	//
	// Workflows-model note (step 11): this used to also apply per-tool
	// wrapper images via DefaultExecutorImages. That mapping is gone —
	// the main image comes verbatim from spec.container.image and is
	// treated as fully qualified.
	ContentFetcherImage string

	// ImageRegistry, if set, is prepended to ContentFetcherImage ONLY —
	// it no longer touches the main container's image (which comes from
	// spec.container.image verbatim). Intended for air-gapped clusters
	// that mirror the content-fetcher into an internal registry.
	ImageRegistry string

	// MinIO configures the artifact scraper (step 07). When Endpoint is set,
	// the compiler adds:
	//   - envFrom on the wrapper container pointing at SecretName (holds
	//     S3-standard AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY keys);
	//   - literal MINIO_ENDPOINT + MINIO_BUCKET env vars on the wrapper.
	// When Endpoint is empty, the wrapper skips scraping and the controller
	// falls back to NoResultReader. Leaves compile output identical to pre-07.
	MinIO MinIOOptions
}

// MinIOOptions groups the MinIO/S3 config the compiler injects into the
// wrapper container's env. Populated by cmd/operator from --minio-* flags.
type MinIOOptions struct {
	Endpoint   string // host:port, no scheme
	Bucket     string // default: MinIODefaultBucket
	SecretName string // Secret in the run's namespace holding S3 creds
	UseSSL     bool   // toggle https:// on the MinIO client
}

// MinIODefaultBucket is what cmd/operator uses when --minio-bucket is empty.
const MinIODefaultBucket = "kubetest-artifacts"

// MinIO-facing env var names on the wrapper container. Public consts so the
// wrapper (pkg/executor, internal/scraper) reads by the same names.
const (
	EnvMinIOEndpoint = "MINIO_ENDPOINT"
	EnvMinIOBucket   = "MINIO_BUCKET"
	EnvMinIOUseSSL   = "MINIO_USE_SSL"
)

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
//   - err: typed via errors.Is (ErrNilTest, ErrNilTestRun,
//     ErrMissingContentFetcherImage). (ErrUnknownExecutor was retired in
//     the step-11 workflows refactor along with spec.type.)
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

	// Workflows model (step 11): image + invocation come verbatim from
	// spec.container. Webhook has already enforced image != "" and at
	// least one of command/args being set, so the compiler doesn't
	// re-validate — a compile error here means a webhook regression.
	image := test.Spec.Container.Image
	command := append([]string(nil), test.Spec.Container.Command...)
	args := append([]string(nil), test.Spec.Container.Args...)

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

	volumes := make([]corev1.Volume, 0, 4+len(mergedPod.Volumes))
	volumes = append(volumes,
		corev1.Volume{Name: VolumeData, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: VolumeResult, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		// KubetestBin holds /entry — the init container copies its own
		// /entry into this shared emptyDir, the main container mounts it
		// and executes /kubetest-bin/entry. See package doc.
		corev1.Volume{Name: VolumeKubetestBin, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: VolumeRequest, VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				Items: []corev1.KeyToPath{
					{Key: RequestFileName, Path: RequestFileName},
					{Key: executor.ContentFileName, Path: executor.ContentFileName},
				},
			},
		}},
	)
	volumes = append(volumes, mergedPod.Volumes...)

	// Content-fetcher image (with optional ImageRegistry prefix).
	fetcherImage := opts.ContentFetcherImage
	if opts.ImageRegistry != "" {
		fetcherImage = opts.ImageRegistry + "/" + fetcherImage
	}

	// Init containers — ORDER MATTERS. The install step must run FIRST so
	// /kubetest-bin/entry exists when subsequent init containers (or the
	// main container) reference it. content-fetcher runs second with the
	// same image (its own /entry does both jobs: cp and fetch).
	initContainers := []corev1.Container{
		{
			// Install /entry into the shared emptyDir. Uses exec-form sh:
			// content-fetcher image is Alpine (has /bin/sh), and this is
			// the ONE place a shell is acceptable — a two-argv cp is
			// simpler than baking an "install" subcommand into /entry.
			Name:    ContainerKubetestBinInstall,
			Image:   fetcherImage,
			Command: []string{"sh", "-c", "cp " + ContentFetcherEntrySrc + " " + KubetestBinMountPath + "/entry"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: VolumeKubetestBin, MountPath: KubetestBinMountPath},
			},
		},
		{
			Name:    ContainerContentFetcher,
			Image:   fetcherImage,
			Command: []string{executor.EntryCommand, "fetch"},
			Env:     initContainerEnv(test.Spec.Content.Git),
			VolumeMounts: []corev1.VolumeMount{
				{Name: VolumeData, MountPath: DataDirPath},
				{Name: VolumeRequest, MountPath: RequestMountDir, ReadOnly: true},
			},
		},
	}

	wrapperEnv := make([]corev1.EnvVar, 0, 4+len(test.Spec.Container.Env))
	wrapperEnv = append(wrapperEnv,
		corev1.EnvVar{Name: executor.EnvDataDir, Value: DataDirPath},
		corev1.EnvVar{Name: executor.EnvResultDir, Value: ResultDirPath},
		corev1.EnvVar{Name: executor.EnvRunID, Value: run.Name},
		corev1.EnvVar{Name: executor.EnvTestRef, Value: run.Spec.TestRef},
	)
	// MinIO config (step 07): only injected when the operator is configured
	// with --minio-endpoint. Wrapper's cmd/entry checks $MINIO_ENDPOINT and
	// skips scraping when unset — so a step 06 cluster keeps working.
	if opts.MinIO.Endpoint != "" {
		bucket := opts.MinIO.Bucket
		if bucket == "" {
			bucket = MinIODefaultBucket
		}
		wrapperEnv = append(wrapperEnv,
			corev1.EnvVar{Name: EnvMinIOEndpoint, Value: opts.MinIO.Endpoint},
			corev1.EnvVar{Name: EnvMinIOBucket, Value: bucket},
		)
		if opts.MinIO.UseSSL {
			wrapperEnv = append(wrapperEnv, corev1.EnvVar{Name: EnvMinIOUseSSL, Value: "true"})
		}
	}
	wrapperEnv = append(wrapperEnv, test.Spec.Container.Env...)

	// envFrom is separate from env: keeps the wrapper container's env-var
	// name/value listing free of credentials in kubectl describe. Compiler
	// only projects the ref; k8s resolves at pod-start.
	var wrapperEnvFrom []corev1.EnvFromSource
	if opts.MinIO.Endpoint != "" && opts.MinIO.SecretName != "" {
		wrapperEnvFrom = append(wrapperEnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: opts.MinIO.SecretName},
			},
		})
	}
	// User-provided envFrom (test.spec.container.envFrom) is appended after
	// the operator's so user keys win on conflict — same precedence as env.
	wrapperEnvFrom = append(wrapperEnvFrom, test.Spec.Container.EnvFrom...)

	wrapperMounts := make([]corev1.VolumeMount, 0, 4+len(test.Spec.Container.VolumeMounts))
	wrapperMounts = append(wrapperMounts,
		corev1.VolumeMount{Name: VolumeData, MountPath: DataDirPath},
		corev1.VolumeMount{Name: VolumeRequest, MountPath: RequestMountDir, ReadOnly: true},
		corev1.VolumeMount{Name: VolumeResult, MountPath: ResultDirPath},
		corev1.VolumeMount{Name: VolumeKubetestBin, MountPath: KubetestBinMountPath},
	)
	wrapperMounts = append(wrapperMounts, test.Spec.Container.VolumeMounts...)

	// Workflows model (step 11): the pod runs the tool image verbatim,
	// with Container.Command = [/kubetest-bin/entry] and Container.Args =
	// user-supplied Command⊕Args (in that precedence — /entry itself
	// re-splits into binary+argv based on request.json). No per-tool
	// specialisation in the compiler.
	wrapperContainerArgs := make([]string, 0, len(command)+len(args))
	wrapperContainerArgs = append(wrapperContainerArgs, command...)
	wrapperContainerArgs = append(wrapperContainerArgs, args...)

	wrapper := corev1.Container{
		Name:            ContainerWrapper,
		Image:           image,
		Command:         []string{KubetestBinEntry},
		Args:            wrapperContainerArgs,
		Env:             wrapperEnv,
		EnvFrom:         wrapperEnvFrom,
		WorkingDir:      test.Spec.Container.WorkingDir,
		ImagePullPolicy: test.Spec.Container.ImagePullPolicy,
		Resources:       test.Spec.Container.Resources,
		SecurityContext: test.Spec.Container.SecurityContext,
		VolumeMounts:    wrapperMounts,
	}

	automountSAToken := false
	backoffLimit := int32(0)
	ttl := TTLSecondsAfterFinished

	// Propagate the tool label (if present on Test) to Job + Pod. Purely
	// additive: reserved-prefix rules from step 03 still forbid users
	// overriding LabelRunID / LabelManagedBy via podLabels.
	jobLabels := map[string]string{
		LabelRunID:     run.Name,
		LabelManagedBy: ManagedByValue,
	}
	if tool := test.Labels[LabelKubetestTool]; tool != "" {
		jobLabels[LabelKubetestTool] = tool
		if podLabels == nil {
			podLabels = map[string]string{}
		}
		podLabels[LabelKubetestTool] = tool
	}

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            run.Name,
			Namespace:       run.Namespace,
			OwnerReferences: ownerRefForTestRun(run),
			Labels:          jobLabels,
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
					RestartPolicy:                 corev1.RestartPolicyNever,
					AutomountServiceAccountToken:  &automountSAToken,
					TerminationGracePeriodSeconds: ptrInt64(terminationGracePeriodSeconds()),
					ServiceAccountName:            mergedPod.ServiceAccountName,
					NodeSelector:                  mergedPod.NodeSelector,
					Tolerations:                   mergedPod.Tolerations,
					Affinity:                      mergedPod.Affinity,
					SecurityContext:               mergedPod.SecurityContext,
					ImagePullSecrets:              mergedPod.ImagePullSecrets,
					InitContainers:                initContainers,
					Containers:                    []corev1.Container{wrapper},
					Volumes:                       volumes,
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
	if v := test.Spec.Verdict; v != nil {
		req.Verdict = executor.VerdictSpec{
			From:         v.From,
			ErrorRateMax: v.ErrorRateMax,
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
