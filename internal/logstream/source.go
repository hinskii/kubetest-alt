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

package logstream

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// K8sLogSource is the production PodLogSource impl backed by client-go.
// Kept tiny so tests avoid instantiating it — everything under test uses
// a fake PodLogSource.
//
// The returned ReadCloser is follow=true (see §15.4): the tailer expects
// Read to block until new bytes arrive rather than immediately returning
// io.EOF on exhaustion. EOF here means "pod terminated, no more logs."
type K8sLogSource struct {
	// Client is any kubernetes.Interface. cmd/operator constructs one from
	// ctrl.GetConfigOrDie(); tests never touch this type.
	Client kubernetes.Interface
}

// Open implements PodLogSource. Streams follow=true from the FIRST line —
// TailLines nil is deliberate: §15.4 forbids relying on end-of-run
// GetLogs, so we tail from start and flush continuously into MinIO before
// kubelet rotation can drop the head of a chatty run.
func (s *K8sLogSource) Open(ctx context.Context, namespace, podName string) (io.ReadCloser, error) {
	if s.Client == nil {
		return nil, fmt.Errorf("k8s log source: nil client")
	}
	req := s.Client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})
	return req.Stream(ctx)
}
