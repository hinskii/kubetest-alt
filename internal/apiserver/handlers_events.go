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
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// maxRunEvents bounds GET /runs/{id}/events (the newest are kept).
const maxRunEvents = 200

// listRunEvents serves GET /runs/{id}/events: what Kubernetes says about
// the run's Job, pods, workers and services — "Pulling image …",
// "FailedScheduling", "BackOff" — so a run that sits in queued says why.
// Objects are the run's when their name is the run's name or starts with
// it plus "-", unless they belong to another run with that longer name
// (load vs load-2, a composite's children).
func (s *Server) listRunEvents(w http.ResponseWriter, r *http.Request) {
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	ref, err := s.findRun(r.Context(), ns, r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	reader := s.EventsReader
	if reader == nil {
		reader = s.K8sClient
	}
	var events corev1.EventList
	if err := reader.List(r.Context(), &events, client.InNamespace(ref.Namespace)); err != nil {
		writeAPIError(w, err)
		return
	}
	var runs testsv1alpha1.TestRunList
	if err := s.K8sClient.List(r.Context(), &runs, client.InNamespace(ref.Namespace)); err != nil {
		writeAPIError(w, err)
		return
	}
	var others []string // longer run names sharing the prefix
	for _, run := range runs.Items {
		if run.Name != ref.Name && strings.HasPrefix(run.Name, ref.Name+"-") {
			others = append(others, run.Name)
		}
	}
	owns := func(name string) bool {
		if name != ref.Name && !strings.HasPrefix(name, ref.Name+"-") {
			return false
		}
		for _, o := range others {
			if name == o || strings.HasPrefix(name, o+"-") {
				return false
			}
		}
		return true
	}
	out := []apiclient.RunEvent{}
	for _, e := range events.Items {
		if !owns(e.InvolvedObject.Name) || e.InvolvedObject.Kind == "TestRun" {
			continue
		}
		out = append(out, apiclient.RunEvent{
			Time: eventTime(&e), Type: e.Type, Reason: e.Reason,
			Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, Message: e.Message, Count: e.Count,
		})
	}
	slices.SortStableFunc(out, func(a, b apiclient.RunEvent) int { return a.Time.Compare(b.Time) })
	if len(out) > maxRunEvents {
		out = out[len(out)-maxRunEvents:]
	}
	writeJSON(w, http.StatusOK, out)
}

// eventTime is when an event last happened (events.k8s.io fields first).
func eventTime(e *corev1.Event) time.Time {
	switch {
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.UTC()
	case !e.EventTime.IsZero():
		return e.EventTime.UTC()
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.UTC()
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.UTC()
	}
	return e.CreationTimestamp.UTC()
}
