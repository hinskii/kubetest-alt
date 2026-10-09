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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase mirrors Testkube's TestWorkflowStatus enum (verified in source),
// plus `error` for infra failures (OOMKill, ImagePullBackOff, missing result) — see CLAUDE.md §15.
// +kubebuilder:validation:Enum=queued;running;paused;passed;failed;aborted;error
type Phase string

const (
	PhaseQueued  Phase = "queued"
	PhaseRunning Phase = "running"
	PhasePaused  Phase = "paused"
	PhasePassed  Phase = "passed"
	PhaseFailed  Phase = "failed"
	PhaseAborted Phase = "aborted"
	PhaseError   Phase = "error"
)

// PodConfig is a generic, unopinionated passthrough to the execution Pod.
// Annotations and labels land verbatim on the pod — the platform hardcodes
// none and special-cases none (CLAUDE.md §8). Validation MUST NOT reject or
// mutate any annotation/label key.
type PodConfig struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// ContainerConfig captures per-step container overrides. Defaults defined at
// TestSpec.container merge with per-step overrides in later steps.
type ContainerConfig struct {
	// +optional
	Image string `json:"image,omitempty"`
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	// +optional
	WorkingDir string `json:"workingDir,omitempty"`
	// +optional
	Command []string `json:"command,omitempty"`
	// +optional
	Args []string `json:"args,omitempty"`
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// Content is how a Test's code and data arrive in the pod (step 20i). The
// code comes from exactly ONE source — git, inline files or a tarball —
// and lands in /data/repo; test data from ConfigMaps and Secrets is
// separate (testData) and lands in /data/testdata/<name>, or where the
// entry says. The validating webhook enforces the single code source.
type Content struct {
	// Git: the code is a repository, checked out at /data/repo.
	// +optional
	Git *GitContent `json:"git,omitempty"`
	// Files: the code is typed into the Test, each file at
	// /data/repo/<path>.
	// +optional
	Files []FileContent `json:"files,omitempty"`
	// +optional
	Tarball []Tarball `json:"tarball,omitempty"`
	// TestData: ConfigMaps and Secrets mounted read-only as files, next to
	// the code from any source.
	// +optional
	TestData []TestDataSource `json:"testData,omitempty"`
}

// TestDataSource mounts one ConfigMap or Secret of the Test's namespace as
// files: by default every key at /data/testdata/<name>/<key>; with
// mountPath every key in that directory; with items the chosen keys at
// exact paths (the only way to replace a file of the code). A mountPath
// over a non-empty directory of the code fails the run instead of hiding
// its files. Field names follow Kubernetes volumes.
type TestDataSource struct {
	// ConfigMap is the name of a ConfigMap. Exactly one of configMap and
	// secret.
	// +optional
	ConfigMap string `json:"configMap,omitempty"`
	// Secret is the name of a Secret.
	// +optional
	Secret string `json:"secret,omitempty"`
	// MountPath is the directory the keys appear in, as files (absolute).
	// +optional
	MountPath string `json:"mountPath,omitempty"`
	// Items are single keys at exact file paths (absolute). Not with
	// mountPath.
	// +optional
	Items []TestDataItem `json:"items,omitempty"`
}

// TestDataItem places one key of a ConfigMap or Secret at a file path.
type TestDataItem struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// GitContent points at a git repository. Auth is secret-backed; no plaintext
// credentials on the CR.
type GitContent struct {
	URI string `json:"uri"`
	// +optional
	Revision string `json:"revision,omitempty"`
	// +optional
	Paths []string `json:"paths,omitempty"`
	// MountPath is where the repository is checked out: relative to /data
	// or absolute inside it. Defaults to /data/repo, where catalog
	// templates look for files.
	// +optional
	MountPath string `json:"mountPath,omitempty"`
	// +kubebuilder:validation:Enum=basic;header;ssh
	// +optional
	AuthType string `json:"authType,omitempty"`
	// +optional
	UsernameFrom *corev1.EnvVarSource `json:"usernameFrom,omitempty"`
	// +optional
	TokenFrom *corev1.EnvVarSource `json:"tokenFrom,omitempty"`
	// +optional
	SSHKeyFrom *corev1.EnvVarSource `json:"sshKeyFrom,omitempty"`
}

// FileContent carries an inline file of a Test's code, at /data/repo/<path>.
// The aggregate size of all inline files on a Test is bounded by the
// validating webhook (see CLAUDE.md §15.7).
type FileContent struct {
	// Path is relative to /data/repo (e.g. load.js, tests/smoke.spec.js).
	Path string `json:"path"`
	// +optional
	Content string `json:"content,omitempty"`
	// ContentFrom is not supported: it never delivered a file (step 20i
	// found the fetcher couldn't read it). The webhook refuses it and points
	// to content.testData; it stays in the schema so it is refused, not
	// silently pruned.
	// +optional
	ContentFrom *corev1.EnvVarSource `json:"contentFrom,omitempty"`
	// +optional
	Mode *int32 `json:"mode,omitempty"`
}

// Tarball fetches a compressed archive over HTTP(S) and unpacks it inside the pod.
type Tarball struct {
	URL string `json:"url"`
	// +optional
	Path string `json:"path,omitempty"`
	// +optional
	Mount bool `json:"mount,omitempty"`
}

// Parameter mirrors Testkube's typed ParameterSchema. Missing default => required.
type Parameter struct {
	// +kubebuilder:validation:Enum=string;integer;number;boolean
	Type string `json:"type"`
	// +optional
	Default string `json:"default,omitempty"`
	// +optional
	Enum []string `json:"enum,omitempty"`
	// +optional
	Pattern string `json:"pattern,omitempty"`
	// Description is shown next to the parameter in run forms.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`
	// Path marks the template's main path parameter — where the tool finds
	// a Test's files: "file" (a script, a plan) or "directory" (a
	// project). Never defaulted: a git Test must set it; with inline files
	// a "file" parameter is the first file, and "directory" tools take
	// projects from git only (step 20i). At most one per template.
	// +kubebuilder:validation:Enum=file;directory
	// +optional
	Path string `json:"path,omitempty"`
}

// ArtifactSpec describes files to scrape after a run (globs via doublestar).
type ArtifactSpec struct {
	// +optional
	Paths []string `json:"paths,omitempty"`
	// +optional
	Compress string `json:"compress,omitempty"`
	// Report names the run's main human-readable report (e.g. an HTML
	// dashboard): a path or glob, relative to the working directory like
	// paths, that one of paths must also collect. The first matching
	// artifact (sorted) is the run's report in the API and Control Center.
	// +optional
	Report string `json:"report,omitempty"`
}

// ArtifactRef is a pointer to a scraped artifact in object storage.
// Aligned with pkg/executor.ArtifactRef by JSON tag — the controller mirrors
// the wrapper's ExecutionResult.Artifacts into TestRun.Status.ArtifactRefs
// without a shape translation.
type ArtifactRef struct {
	// Path is the file path relative to the wrapper's working directory.
	Path string `json:"path"`
	// Key is the object-store key
	// ("runs/<namespace>/<runUID>/artifacts/<Path>").
	// +optional
	Key string `json:"key,omitempty"`
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// +optional
	ContentType string `json:"contentType,omitempty"`
}

// ContentStatus mirrors pkg/executor.ContentInfo.
type ContentStatus struct {
	// GitRevision is the revision the Test asked for ("HEAD" when unset).
	// +optional
	GitRevision string `json:"gitRevision,omitempty"`
	// GitCommit is the full commit SHA that was checked out.
	// +optional
	GitCommit string `json:"gitCommit,omitempty"`
}

// TestCounts mirrors pkg/executor.TestCounts — JUnit-aggregated counts the
// scraper (step 07) parses out of uploaded XML fixtures.
type TestCounts struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// RetryPolicy re-runs what failed. On a Test (spec.retry) the wrapper
// runs the tool again inside the same pod while the verdict isn't passed —
// one log, one artifact set, tries listed in status.steps (attempt-N). On
// a composite step (steps[].retry) each failed child gets a new TestRun
// <child>-r<N>. Not retried: aborted runs, a timeout, a tool that can't
// start, pod-level failures (OOM, eviction).
type RetryPolicy struct {
	// Count is how many times to try again after the first try.
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count"`
	// Until is the stop condition: "passed" (the default, the only one
	// supported).
	// +kubebuilder:validation:Enum=passed
	// +optional
	Until string `json:"until,omitempty"`
}

// ServiceSpec is a dependency the test needs running — a database, a mock
// server, a browser grid. The operator starts it before the test as Pods
// <run>-<name>-<i> behind a headless Service <run>-<name>, waits until
// every replica is Ready, runs the test, and removes them when the run
// ends. The test reaches it at {{ services.<name> }} (also env
// KUBETEST_SERVICE_<NAME>_HOST): <run>-<name>.<namespace>.svc, resolving
// to every ready replica; replica i is <run>-<name>-<i>.<run>-<name>.<namespace>.svc.
// The replicas use the Test's spec.pod (annotations, service account,
// scheduling, security context).
type ServiceSpec struct {
	// +required
	Image string `json:"image,omitempty"`
	// Command / Args override the image's entrypoint / cmd.
	// +optional
	Command []string `json:"command,omitempty"`
	// +optional
	Args []string `json:"args,omitempty"`
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// ReadinessProbe decides when a replica is ready; without one a replica
	// is ready once its container runs.
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`
	// Timeout bounds the wait for every replica to be ready (default 5m);
	// past it the run ends as error (ServiceNotReady).
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
	// Logs stores each replica's log next to the run's
	// (runs/<ns>/<uid>/services/<name>-<i>/logs/).
	// +optional
	Logs bool `json:"logs,omitempty"`
	// Count is the number of identical replicas (default 1).
	// +kubebuilder:validation:Minimum=1
	// +optional
	Count *int32 `json:"count,omitempty"`
	// MaxCount is not supported for services (the webhook refuses it).
	// +optional
	MaxCount *int32 `json:"maxCount,omitempty"`
	// Matrix starts one replica per combination of the values (× Count);
	// replica i gets env KUBETEST_MATRIX_<KEY> with its value.
	// +optional
	Matrix map[string][]string `json:"matrix,omitempty"`
	// Shards is not supported for services (the webhook refuses it).
	// +optional
	Shards map[string]string `json:"shards,omitempty"`
	// RestartPolicy of the replica pods (default Always).
	// +kubebuilder:validation:Enum=Always;OnFailure;Never
	// +optional
	RestartPolicy corev1.RestartPolicy `json:"restartPolicy,omitempty"`
}

// ParallelSpec runs the Test as several workers at once, each its own Job
// and pod with its own content checkout, log and artifacts. The run passes
// when every worker passes (any error → error, else any failure → failed);
// each worker's phase is in status.steps["worker-<i>"], its log, result
// and artifacts under the run's artifacts at workers/<i>/.
//
// Workers: Count copies of every Matrix combination. With Shards and no
// Count, each combination gets one worker per value of the longest shard
// list, at most MaxCount. A worker sees its identity as expressions
// {{ worker.index }} / {{ worker.count }} / {{ matrix.<key> }} /
// {{ shard.<key> }} in the container's command, args, env and working dir,
// and as env KUBETEST_WORKER_INDEX, KUBETEST_WORKER_COUNT,
// KUBETEST_MATRIX_<KEY>, KUBETEST_SHARD_<KEY> (shard values comma-joined).
type ParallelSpec struct {
	// Count is the number of workers per matrix combination.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Count *int32 `json:"count,omitempty"`
	// MaxCount caps the workers per combination when Shards decide the
	// number (no Count).
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxCount *int32 `json:"maxCount,omitempty"`
	// Matrix: one worker group per combination of the values.
	// +optional
	Matrix map[string][]string `json:"matrix,omitempty"`
	// Shards splits each list of values into contiguous, near-equal parts,
	// one per worker of a combination: shard.<key> is that worker's part.
	// +optional
	Shards map[string][]string `json:"shards,omitempty"`
}

// StepResult carries per-step timing and phase for TestRunStatus.steps.
//
// Step 17 note: composite runs also fill this map — the key convention
// mirrors the composer's (`s{idx}` for the whole step aggregate,
// `s{idx}/{test}[{i}]` per child reference). `Phase` may be "skipped"
// here even though `Phase` never carries that value at the TestRun
// level. That's a deliberate skip-on-fail marker (rationale in
// internal/composer): skipped is a per-step property, not a run
// property, so we keep the run-level enum stable across 17 steps
// instead of growing a new value.
type StepResult struct {
	// Phase carries the step-level state. Its type is StepPhase (a
	// SUPERSET of the run-level Phase enum with the added "skipped"
	// value) — the two types are deliberately distinct so the CRD
	// enum on TestRun.Status.Phase stays exactly what it was before
	// step 17 (queued/running/paused/passed/failed/aborted/error).
	// The "skipped" marker lives ONLY at the step level; composite
	// aggregation folds skipped steps into ParentPhase's decision
	// without them ever being written into the run-level Phase.
	// +optional
	Phase StepPhase `json:"phase,omitempty"`
	// +optional
	QueuedAt *metav1.Time `json:"queuedAt,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Message explains a step-level verdict the children alone don't
	// (e.g. "step timeout exceeded").
	// +optional
	Message string `json:"message,omitempty"`
}

// StepPhase is the enum for StepResult.Phase. Kept as a separate type
// so we can add "skipped" without widening the run-level Phase enum
// on TestRun.status.phase (that field's CRD schema stays untouched
// across step 17 per plan). Values mirror Phase 1:1 plus "skipped".
// +kubebuilder:validation:Enum=queued;running;paused;passed;failed;aborted;error;skipped
type StepPhase string

// Step-level phase constants. StepPhaseFromPhase converts from the
// run-level Phase so callers can propagate a child's phase into the
// parent's StepResult without importing string literals everywhere.
const (
	StepPhaseQueued  StepPhase = "queued"
	StepPhaseRunning StepPhase = "running"
	StepPhasePaused  StepPhase = "paused"
	StepPhasePassed  StepPhase = "passed"
	StepPhaseFailed  StepPhase = "failed"
	StepPhaseAborted StepPhase = "aborted"
	StepPhaseError   StepPhase = "error"

	// StepPhaseSkipped is the composite skip-on-fail marker — a step
	// that never ran because a prior non-optional step failed. Only
	// valid inside StepResult; NEVER written into TestRun.Status.Phase.
	StepPhaseSkipped StepPhase = "skipped"
)

// StepPhaseFromPhase converts run-level Phase → StepPhase (same
// underlying string). Kept as a helper so callers don't reach for
// unchecked string casts.
func StepPhaseFromPhase(p Phase) StepPhase { return StepPhase(p) }

// RunReference points at the most recent TestRun for a Test.
type RunReference struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}
