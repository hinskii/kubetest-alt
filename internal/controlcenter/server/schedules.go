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

package server

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Schedules (step 18g): recurring runs are kubetest's Test.spec.schedule
// (fired by the operator's scheduler); one-shot runs are TestRuns waiting
// for spec.notBefore. Control Center only reads and edits those — it keeps
// no schedule of its own.

// cronParser is the operator's and the webhook's parser: five fields, an
// optional CRON_TZ=<zone> prefix, UTC otherwise.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// nextFiresShown is how many upcoming fire times a schedule lists.
const nextFiresShown = 3

// maxQueuedListed bounds the queued runs scanned for one-shot schedules.
const maxQueuedListed = 200

// backToSchedules is the abort form's "back" value that returns to the
// schedules page; any other value returns to the run (no open redirect).
const backToSchedules = "schedules"

func schedulesPath(c string) string { return clusterPath(c) + "/schedules" }

// nextFires returns the next n fire times of a cron expression after now,
// in UTC.
func nextFires(expr string, now time.Time, n int) ([]time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, n)
	// Without CRON_TZ the library uses the zone of the time it is given;
	// schedules are UTC (as the operator evaluates them), not CC's zone.
	t := now.UTC()
	for range n {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t.UTC())
	}
	return out, nil
}

// scheduleView is a Test's recurring schedule as the pages show it.
type scheduleView struct {
	Expr string
	Next []time.Time
	Err  string // the expression doesn't parse (never fires)
}

func viewSchedule(expr string, now time.Time) *scheduleView {
	if expr == "" {
		return nil
	}
	v := &scheduleView{Expr: expr}
	next, err := nextFires(expr, now, nextFiresShown)
	if err != nil {
		v.Err = err.Error()
	}
	v.Next = next
	return v
}

type recurringRow struct {
	Name, Namespace, Tool string
	Locked                bool
	Schedule              *scheduleView
	LatestRun             *testsv1alpha1.RunReference
}

type schedulesData struct {
	Cluster   string
	Recurring []recurringRow
	OneShot   []apiclient.Run
	QueuedErr string
	Back      string
}

// schedulesPage lists every recurring schedule and every run waiting for
// its start time in the cluster.
func (s *Server) schedulesPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	client := api(r, c)
	tests, err := client.ListTests(r.Context(), "")
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	now := time.Now()
	data := schedulesData{Cluster: c.Name, Back: backToSchedules}
	for _, t := range tests {
		if t.Spec.Schedule == "" {
			continue
		}
		data.Recurring = append(data.Recurring, recurringRow{
			Name: t.Name, Namespace: t.Namespace, Tool: t.Labels[labelTool],
			Locked:    t.Labels[labelManagedBy] != managedByUI,
			Schedule:  viewSchedule(t.Spec.Schedule, now),
			LatestRun: t.Status.LatestRun,
		})
	}
	slices.SortFunc(data.Recurring, func(a, b recurringRow) int {
		return cmp.Or(firstFire(a).Compare(firstFire(b)), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	// A queued-runs failure still shows the recurring schedules.
	page, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{
		Phase: string(testsv1alpha1.PhaseQueued), Limit: maxQueuedListed})
	if err != nil {
		data.QueuedErr = messageOf(err)
	} else {
		data.OneShot = scheduledRuns(page.Runs, now)
	}
	s.page(w, r, "schedules", "Schedules", clusterCrumbs(c), data)
}

// firstFire orders schedules by their next fire; broken ones go last.
func firstFire(row recurringRow) time.Time {
	if len(row.Schedule.Next) == 0 {
		return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return row.Schedule.Next[0]
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

// setSchedule sets or clears a GUI-managed Test's recurring schedule.
func (s *Server) setSchedule(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	back := testPath(c.Name, ns, name)
	expr := strings.Join(strings.Fields(r.PostFormValue("schedule")), " ")
	if r.PostFormValue("remove") != "" {
		expr = ""
	}
	var next []time.Time
	if expr != "" {
		var err error
		if next, err = nextFires(expr, time.Now(), 1); err != nil {
			redirect(w, r, back, "Not a valid cron expression: "+err.Error())
			return
		}
	}
	if _, err := api(r, c).SetTestSchedule(r.Context(), ns, name, expr); err != nil {
		if apiclient.IsConflict(err) {
			redirect(w, r, back, "This Test is managed in Git: change spec.schedule there.")
			return
		}
		redirect(w, r, back, "Could not save the schedule: "+messageOf(err))
		return
	}
	switch {
	case expr == "":
		redirect(w, r, back, "Schedule removed.")
	case len(next) == 0:
		redirect(w, r, back, "Schedule saved; it never fires.")
	default:
		redirect(w, r, back, "Schedule saved. Next run: "+views.When(next[0])+".")
	}
}
