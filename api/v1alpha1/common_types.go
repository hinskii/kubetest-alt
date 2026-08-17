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

// Content describes how the test payload arrives inside the pod. Sources are
// combined (git files + inline files + tarballs) into a shared /data mount by
// the content fetcher (step 06).
type Content struct {
	// +optional
	Git *GitContent `json:"git,omitempty"`
	// +optional
	Files []FileContent `json:"files,omitempty"`
	// +optional
	Tarball []Tarball `json:"tarball,omitempty"`
}

// GitContent points at a git repository. Auth is secret-backed; no plaintext
// credentials on the CR.
type GitContent struct {
	URI string `json:"uri"`
	// +optional
	Revision string `json:"revision,omitempty"`
	// +optional
	Paths []string `json:"paths,omitempty"`
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

// FileContent carries an inline file. The aggregate size of all inline files
// on a Test is bounded by the validating webhook (see CLAUDE.md §15.7).
type FileContent struct {
	Path string `json:"path"`
	// +optional
	Content string `json:"content,omitempty"`
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
}

// ArtifactSpec describes files to scrape after a run (globs via doublestar).
type ArtifactSpec struct {
	// +optional
	Paths []string `json:"paths,omitempty"`
	// +optional
	Compress string `json:"compress,omitempty"`
}

// ArtifactRef is a pointer to a scraped artifact in object storage.
type ArtifactRef struct {
	Name string `json:"name"`
	// +optional
	Key string `json:"key,omitempty"`
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// +optional
	ContentType string `json:"contentType,omitempty"`
}

// RetryPolicy configures automatic retry on step failure.
type RetryPolicy struct {
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count"`
	// +optional
	Until string `json:"until,omitempty"`
}

// ServiceSpec is a sidecar-like dependent service. Detailed fields land with the
// services runtime (post-v1); step 02 keeps the shape minimal but present so
// GitOps manifests can reference it.
type ServiceSpec struct {
	// +optional
	Image string `json:"image,omitempty"`
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
	// +optional
	Logs bool `json:"logs,omitempty"`
	// +optional
	Count *int32 `json:"count,omitempty"`
	// +optional
	MaxCount *int32 `json:"maxCount,omitempty"`
	// +optional
	Matrix map[string][]string `json:"matrix,omitempty"`
	// +optional
	Shards map[string]string `json:"shards,omitempty"`
	// +optional
	RestartPolicy corev1.RestartPolicy `json:"restartPolicy,omitempty"`
}

// ParallelSpec configures matrix/sharding for parallel execution. Detailed
// fields land in step 06+; step 02 keeps the shape minimal.
type ParallelSpec struct {
	// +optional
	Count *int32 `json:"count,omitempty"`
	// +optional
	MaxCount *int32 `json:"maxCount,omitempty"`
	// +optional
	Matrix map[string][]string `json:"matrix,omitempty"`
	// +optional
	Shards map[string]string `json:"shards,omitempty"`
	// +optional
	Transfer []string `json:"transfer,omitempty"`
	// +optional
	Fetch []string `json:"fetch,omitempty"`
	// +optional
	Logs bool `json:"logs,omitempty"`
}

// StepResult carries per-step timing and phase for TestRunStatus.steps.
type StepResult struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	QueuedAt *metav1.Time `json:"queuedAt,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// RunReference points at the most recent TestRun for a Test.
type RunReference struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}
