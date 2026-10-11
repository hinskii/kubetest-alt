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

package api

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Schedules (step 18g): recurring runs are kubetest's Test.spec.schedule
// (fired by the operator's scheduler); one-time runs are TestRuns waiting
// for spec.notBefore. Control Center only reads and edits those — it keeps
// no schedule of its own.

// maxQueuedListed bounds the queued runs scanned for one-time schedules.
const maxQueuedListed = 200

// ScheduleList is every schedule in a cluster.
type ScheduleList struct {
	// Recurring are the Tests with a schedule, soonest next fire first;
	// schedules that never fire go last.
	Recurring []RecurringSchedule `json:"recurring" required:"true" nullable:"false"`
	// OneTime are the runs waiting for a future start time, soonest first.
	OneTime []ScheduledRun `json:"oneTime" required:"true" nullable:"false"`
	// OneTimeError: the waiting runs couldn't be read (the recurring ones
	// still show).
	OneTimeError string `json:"oneTimeError,omitempty"`
}

// RecurringSchedule is a Test's spec.schedule.
type RecurringSchedule struct {
	Name      string   `json:"name" required:"true"`
	Namespace string   `json:"namespace" required:"true"`
	Tool      string   `json:"tool" required:"true"`
	Locked    bool     `json:"locked" required:"true"`
	Schedule  Schedule `json:"schedule" required:"true"`
	LatestRun *RunRef  `json:"latestRun,omitempty"`
}

// ScheduledRun is a run waiting for its start time.
type ScheduledRun struct {
	ID        string    `json:"id" required:"true"`
	Name      string    `json:"name" required:"true"`
	Namespace string    `json:"namespace" required:"true"`
	TestRef   string    `json:"testRef" required:"true"`
	NotBefore time.Time `json:"notBefore" required:"true"`
	CreatedBy string    `json:"createdBy,omitempty"`
}

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	tests, err := client.ListTests(r.Context(), "")
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	now := time.Now()
	out := ScheduleList{Recurring: []RecurringSchedule{}, OneTime: []ScheduledRun{}}
	for _, t := range tests {
		if t.Spec.Schedule == "" {
			continue
		}
		row := RecurringSchedule{Name: t.Name, Namespace: t.Namespace, Tool: t.Labels[LabelTool],
			Locked: Locked(t.Labels), Schedule: *ScheduleOf(t.Spec.Schedule, now)}
		if lr := t.Status.LatestRun; lr != nil {
			row.LatestRun = &RunRef{Name: lr.Name, Phase: string(lr.Phase)}
			if lr.FinishedAt != nil {
				at := lr.FinishedAt.UTC()
				row.LatestRun.FinishedAt = &at
			}
		}
		out.Recurring = append(out.Recurring, row)
	}
	slices.SortFunc(out.Recurring, func(a, b RecurringSchedule) int {
		return cmp.Or(firstFire(a.Schedule).Compare(firstFire(b.Schedule)),
			cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	page, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{
		Phase: string(testsv1alpha1.PhaseQueued), Limit: maxQueuedListed})
	if err != nil {
		out.OneTimeError = messageOf(err)
	} else {
		for _, run := range scheduledRuns(page.Runs, now) {
			out.OneTime = append(out.OneTime, ScheduledRun{ID: RunID(run), Name: run.Name, Namespace: run.Namespace,
				TestRef: run.TestRef, NotBefore: run.NotBefore.UTC(), CreatedBy: run.Tags[TagCreatedBy]})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// firstFire orders schedules by their next fire; broken ones go last.
func firstFire(s Schedule) time.Time {
	if len(s.Next) == 0 {
		return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return s.Next[0]
}

// scheduledRuns returns the runs still waiting for a future start time,
// soonest first.
func scheduledRuns(runs []apiclient.Run, now time.Time) []apiclient.Run {
	var out []apiclient.Run
	for _, run := range runs {
		if run.Phase == string(testsv1alpha1.PhaseQueued) && run.NotBefore != nil && run.NotBefore.After(now) && run.Abort == nil {
			out = append(out, run)
		}
	}
	slices.SortFunc(out, func(a, b apiclient.Run) int { return a.NotBefore.Compare(*b.NotBefore) })
	return out
}
