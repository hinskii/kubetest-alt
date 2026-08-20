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
	"k8s.io/apimachinery/pkg/runtime"
)

// WebhookHeader carries one HTTP header for outbound delivery. Values may
// come inline (Value, plaintext) or from a Secret (ValueFrom.SecretKeyRef).
// Any header sourced from a secret is REDACTED in every log line the
// dispatcher emits — see internal/webhookdelivery. Prefer ValueFrom for
// bearer tokens, HMAC secrets, and anything else the operator should not
// be able to spill into stdout.
type WebhookHeader struct {
	Name string `json:"name"`

	// +optional
	Value string `json:"value,omitempty"`

	// +optional
	ValueFrom *corev1.EnvVarSource `json:"valueFrom,omitempty"`
}

// WebhookSpec describes an outbound webhook destination.
//
// The dispatcher picks up subscribers per event and delivers ASYNC —
// endpoint latency never delays a reconcile. Retries with capped
// exponential backoff on 5xx / connection errors; 4xx is permanent
// (never retried; the endpoint is telling us NOT to try again).
type WebhookSpec struct {
	// URL is the HTTP endpoint to POST payloads to. Required.
	URL string `json:"url"`

	// Events filters which lifecycle transitions the dispatcher delivers.
	// Empty = deliver every terminal transition. Enum values match the
	// TestRun.Status.Phase strings so consumers speak one dialect.
	// +kubebuilder:validation:items:Enum=queued;running;paused;passed;failed;aborted;error
	// +optional
	Events []string `json:"events,omitempty"`

	// Headers to attach to every outbound POST. Value can come inline
	// or from a Secret via valueFrom.secretKeyRef. Secret-sourced values
	// are redacted in dispatcher logs.
	// +optional
	Headers []WebhookHeader `json:"headers,omitempty"`

	// TimeoutSeconds bounds each HTTP request (client-side deadline). 0
	// defaults to 10s. Also bounds each retry attempt.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=120
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// MaxRetries bounds the retry count on 5xx / connection errors. 0
	// defaults to 5. 4xx responses are ALWAYS permanent regardless of
	// this value.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`
}

// WebhookStatus tracks last-delivery outcomes for observability. Populated
// by the dispatcher.
type WebhookStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastDeliveryAt is the wall-clock time of the most recent HTTP
	// attempt (regardless of outcome).
	// +optional
	LastDeliveryAt *metav1.Time `json:"lastDeliveryAt,omitempty"`

	// LastDeliveryOutcome is one of "success", "failed", "no-retry-4xx",
	// "timeout". Empty until the first delivery.
	// +optional
	LastDeliveryOutcome string `json:"lastDeliveryOutcome,omitempty"`

	// DeliveriesTotal / FailuresTotal are simple counters for at-a-glance
	// health. Real observability lives in Prometheus (webhook_deliveries_total).
	// +optional
	DeliveriesTotal int64 `json:"deliveriesTotal,omitempty"`
	// +optional
	FailuresTotal int64 `json:"failuresTotal,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Last",type=string,JSONPath=`.status.lastDeliveryOutcome`

// Webhook is a single outbound delivery destination. Multiple Webhooks
// stack — every subscriber matching the run's event gets its own
// delivery attempt.
type Webhook struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec WebhookSpec `json:"spec"`

	// +optional
	Status WebhookStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WebhookList contains a list of Webhook.
type WebhookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Webhook `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Webhook{}, &WebhookList{})
		return nil
	})
}
