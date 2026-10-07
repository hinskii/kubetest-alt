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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

func k8sEvent(name, kind, obj, reason, typ string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: obj, Namespace: "default"},
		Reason:         reason, Type: typ, Message: reason + " " + obj,
		LastTimestamp: metav1.NewTime(at), Count: 1,
	}
}

// series makes e a repeating event last seen at last (events.k8s.io).
func series(e *corev1.Event, last time.Time) *corev1.Event {
	e.Series = &corev1.EventSeries{Count: 5, LastObservedTime: metav1.NewMicroTime(last)}
	return e
}

func TestRunEvents_TheRunsObjectsOnly(t *testing.T) {
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	child := liveRun("load-s0-leaf-0", testsv1alpha1.PhaseRunning)
	child.Labels = map[string]string{store.LabelParentRun: "load"}
	s, _, _ := mkStorageServer(t, nil,
		liveRun("load", testsv1alpha1.PhaseQueued), liveRun("load-2", testsv1alpha1.PhaseRunning), child)
	for _, e := range []*corev1.Event{
		k8sEvent("e3", "Pod", "load-x7k2p", "Pulling", "Normal", base.Add(2*time.Second)),
		k8sEvent("e1", "Job", "load", "SuccessfulCreate", "Normal", base),
		k8sEvent("e2", "Pod", "load-x7k2p", "Scheduled", "Normal", base.Add(time.Second)),
		k8sEvent("e4", "Pod", "load-x7k2p", "Failed", "Warning", base.Add(3*time.Second)),
		series(k8sEvent("e5", "Pod", "load-x7k2p", "BackOff", "Warning", base), base.Add(4*time.Second)),
		k8sEvent("o1", "Pod", "load-2-abcde", "Pulling", "Normal", base),         // another run
		k8sEvent("o2", "Pod", "load-s0-leaf-0-zzzzz", "Pulling", "Normal", base), // a child run
		k8sEvent("o3", "Pod", "loader-abcde", "Pulling", "Normal", base),         // not a prefix match
	} {
		require.NoError(t, s.K8sClient.Create(t.Context(), e))
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/load/events", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got []apiclient.RunEvent
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	reasons := make([]string, 0, len(got))
	for _, e := range got {
		reasons = append(reasons, e.Reason)
	}
	assert.Equal(t, []string{"SuccessfulCreate", "Scheduled", "Pulling", "Failed", "BackOff"}, reasons,
		"oldest first (a repeating event by its last occurrence), only load's")
	assert.Equal(t, "Pod/load-x7k2p", got[3].Object)
	assert.Equal(t, "Warning", got[3].Type)

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/nope/events", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListRuns_ParentFilter(t *testing.T) {
	child := liveRun("suite-s0-a-0", testsv1alpha1.PhaseRunning)
	child.Labels = map[string]string{store.LabelParentRun: "suite"}
	archived := archivedRow("suite-s1-b-0")
	archived.ParentRun = "suite"
	s, _, _ := mkStorageServer(t, []store.Row{archived},
		liveRun("suite", testsv1alpha1.PhaseRunning), child, liveRun("other", testsv1alpha1.PhaseRunning))
	page, _ := listPage(t, s.Handler(), "?parent=suite")
	names := make([]string, 0, len(page))
	for _, r := range page {
		names = append(names, r.Name)
	}
	assert.ElementsMatch(t, []string{"suite-s0-a-0", "suite-s1-b-0"}, names, "live and archived children, nothing else")
}
