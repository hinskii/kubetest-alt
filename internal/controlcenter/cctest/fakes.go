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

package cctest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// UIDs of the runs Cases knows: the current run and an earlier one.
const (
	RunUID  = "11111111-2222-3333-4444-555555555555"
	BaseUID = "99999999-0000-0000-0000-000000000001"
)

// Cases plays the run store's test-case side for the real API server:
// the current run (RunUID) against an earlier one (BaseUID).
type Cases struct{}

// suite is the class every canned case belongs to.
const suite = "shop"

func caseRow(name, status string, ms int64, msg string) store.CaseRow {
	return store.CaseRow{Key: suite + " › " + name, TestCase: executor.TestCase{
		Class: suite, Name: name, Status: status, DurationMs: ms, Message: msg, Details: "at " + name}}
}

// RunCases implements apiserver.CaseReader.
func (Cases) RunCases(_ context.Context, uid string, failedOnly bool) ([]store.CaseRow, error) {
	var rows []store.CaseRow
	switch uid {
	case RunUID:
		rows = []store.CaseRow{caseRow("login", executor.CasePassed, 100, ""), caseRow("checkout", executor.CaseFailed, 900, "timeout after 5s"),
			caseRow("refund", executor.CaseFailed, 50, "500"), caseRow("search", executor.CasePassed, 40, ""), caseRow("wishlist", executor.CasePassed, 10, "")}
	case BaseUID:
		rows = []store.CaseRow{caseRow("login", executor.CasePassed, 120, ""), caseRow("checkout", executor.CasePassed, 300, ""),
			caseRow("refund", executor.CaseFailed, 50, "500"), caseRow("search", executor.CaseFailed, 40, "no results"), caseRow("legacy", executor.CasePassed, 5, "")}
	}
	if failedOnly {
		var out []store.CaseRow
		for _, r := range rows {
			if r.Status == executor.CaseFailed || r.Status == executor.CaseError {
				out = append(out, r)
			}
		}
		return out, nil
	}
	return rows, nil
}

// CaseStats implements apiserver.CaseReader.
func (Cases) CaseStats(context.Context, string, string, int) ([]store.CaseStats, error) {
	return []store.CaseStats{
		{Key: "shop › refund", Class: suite, Name: "refund", Runs: 10, Failed: 10, LastStatus: executor.CaseFailed, AvgMs: 50},
		{Key: "shop › checkout", Class: suite, Name: "checkout", Runs: 10, Passed: 6, Failed: 4, Flips: 7,
			LastStatus: executor.CaseFailed, AvgMs: 600, MaxMs: 900},
		{Key: "shop › login", Class: suite, Name: "login", Runs: 10, Passed: 10, LastStatus: executor.CasePassed, AvgMs: 110},
	}, nil
}

// CaseHistory implements apiserver.CaseReader.
func (Cases) CaseHistory(_ context.Context, _, _, _ string, _ int) ([]store.CaseRun, error) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	return []store.CaseRun{
		{RunUID: RunUID, RunName: "smoke-abcde", FinishedAt: at, Status: executor.CaseFailed, Message: "timeout after 5s"},
		{RunUID: BaseUID, RunName: "smoke-older", FinishedAt: at.Add(-time.Hour), Status: executor.CasePassed},
	}, nil
}

// Archive is run history for the real API server: rows whose TestRun is
// gone. Paging is not modelled (every row fits on one page).
type Archive struct {
	mu   sync.Mutex
	Rows map[string]store.Row
}

// Get implements apiserver.RunReader.
func (a *Archive) Get(_ context.Context, uid string) (*store.Row, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.Rows[uid]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

// GetByName implements apiserver.RunReader.
func (a *Archive) GetByName(_ context.Context, namespace, name string) (*store.Row, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.Rows {
		if r.Namespace == namespace && r.Name == name {
			return &r, nil
		}
	}
	return nil, store.ErrNotFound
}

// List implements apiserver.RunReader.
func (a *Archive) List(_ context.Context, f store.Filter, p store.Page) ([]store.Row, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.After != "" {
		return nil, nil
	}
	var out []store.Row
	for _, r := range a.Rows {
		if (f.TestRef == "" || r.TestRef == f.TestRef) && (f.Namespace == "" || r.Namespace == f.Namespace) &&
			(f.Phase == "" || r.Phase == f.Phase) && (f.ParentRun == "" || r.ParentRun == f.ParentRun) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(x, y store.Row) int { return y.FinishedAt.Compare(x.FinishedAt) })
	return out, nil
}

// Delete implements apiserver.RunDeleter.
func (a *Archive) Delete(_ context.Context, uid string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.Rows[uid]; !ok {
		return store.ErrNotFound
	}
	delete(a.Rows, uid)
	return nil
}

// SetComment implements apiserver.RunCommenter (nil removes it).
func (a *Archive) SetComment(_ context.Context, uid string, c *store.Comment) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.Rows[uid]
	if !ok {
		return store.ErrNotFound
	}
	r.Comment = c
	a.Rows[uid] = r
	return nil
}
