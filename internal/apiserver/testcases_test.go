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
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

type fakeCases struct {
	gotFailedOnly bool
	gotWindow     int
	gotNS, gotKey string
}

func (f *fakeCases) RunCases(_ context.Context, uid string, failedOnly bool) ([]store.CaseRow, error) {
	f.gotFailedOnly = failedOnly
	if uid != testUID("old") {
		return []store.CaseRow{}, nil
	}
	return []store.CaseRow{{Key: "shop › checkout", TestCase: executor.TestCase{
		Class: "shop", Name: "checkout", Status: executor.CaseFailed, Message: "timeout", Details: "stack"}}}, nil
}

func (f *fakeCases) CaseStats(_ context.Context, ns, _ string, window int) ([]store.CaseStats, error) {
	f.gotNS, f.gotWindow = ns, window
	return []store.CaseStats{{Key: "shop › checkout", Name: "checkout", Runs: 4, Passed: 2, Failed: 2, Flips: 3}}, nil
}

func (f *fakeCases) CaseHistory(_ context.Context, _, _, key string, _ int) ([]store.CaseRun, error) {
	f.gotKey = key
	return []store.CaseRun{{RunUID: "u", RunName: "r", Status: "failed", FinishedAt: time.Unix(0, 0)}}, nil
}

func TestTestCases_ThroughTheClient(t *testing.T) {
	s, _, _ := mkStorageServer(t, []store.Row{archivedRow("old")})
	fc := &fakeCases{}
	s.Cases = fc
	c := mkContractClient(t, s)
	ctx := t.Context()

	cases, err := c.RunTestCases(ctx, "", testUID("old"), true)
	require.NoError(t, err)
	assert.True(t, fc.gotFailedOnly)
	require.Len(t, cases, 1)
	assert.Equal(t, apiclient.TestCase{Key: "shop › checkout", Class: "shop", Name: "checkout",
		Status: "failed", Message: "timeout", Details: "stack"}, cases[0])

	_, err = c.RunTestCases(ctx, "", "ghost", false)
	assert.True(t, apiclient.IsNotFound(err), "unknown run")

	stats, err := c.TestCaseStats(ctx, "", "smoke", 0)
	require.NoError(t, err)
	assert.Equal(t, defaultCaseWindow, fc.gotWindow)
	assert.Equal(t, "default", fc.gotNS)
	require.Len(t, stats, 1)
	assert.True(t, stats[0].Flaky, "passed and failed in the window")
	assert.Equal(t, 3, stats[0].Flips)

	_, err = c.TestCaseStats(ctx, "", "smoke", 500)
	var apiErr *apiclient.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusBadRequest, apiErr.Status)

	hist, err := c.TestCaseHistory(ctx, "", "smoke", "shop › checkout", 0)
	require.NoError(t, err)
	assert.Equal(t, "shop › checkout", fc.gotKey)
	assert.Len(t, hist, 1)
}

func TestTestCases_NoStore(t *testing.T) {
	s, _ := mkServer(t, liveRun("r1", testsv1alpha1.PhasePassed))
	c := mkContractClient(t, s)
	_, err := c.RunTestCases(t.Context(), "", "r1", false)
	var apiErr *apiclient.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.Status)
	assert.Equal(t, http.StatusServiceUnavailable, get(t, s.Handler(), "/tests/smoke/testcases/history?case=x").Code)

	s.Cases = &fakeCases{}
	assert.Equal(t, http.StatusBadRequest, get(t, s.Handler(), "/tests/smoke/testcases/history").Code, "case is required")
}
