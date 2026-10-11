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
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/cctest"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// casesRun is the run cctest.Cases knows as the current one (2 of 5 failed).
func casesRun() *testsv1alpha1.TestRun {
	r := finished("smoke-abcde", testsv1alpha1.PhaseFailed)
	r.Status.TestCounts = &testsv1alpha1.TestCounts{Total: 5, Passed: 3, Failed: 2}
	return r
}

// olderRun is the run cctest.Cases knows as the earlier one.
func olderRun() *testsv1alpha1.TestRun {
	r := runOf("smoke-older", time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC))
	r.UID = types.UID(cctest.BaseUID)
	r.Status.Phase = testsv1alpha1.PhaseFailed
	earlier := metav1.NewTime(time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC))
	r.Status.FinishedAt = &earlier
	return r
}

func TestListCases(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest())
	rec := w.do(t, http.MethodGet, smokePath+"/cases", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeAs[CaseList](t, rec)
	assert.Equal(t, 30, got.Window, "default window")
	assert.Equal(t, []int{10, 30, 100}, got.Windows)
	assert.Equal(t, 2, got.FailingNow)
	assert.Equal(t, 1, got.Flaky, "the API marks checkout (passed and failed)")
	require.Len(t, got.Cases, 3)
	assert.Equal(t, "refund", got.Cases[0].Name, "most failures first (the API's order)")
	assert.Equal(t, CaseStat{Key: "shop › checkout", Name: "checkout", Class: "shop", Runs: 10, Passed: 6, Failed: 4,
		Flips: 7, Flaky: true, LastStatus: "failed", AvgMs: 600, MaxMs: 900}, got.Cases[1])

	assert.Equal(t, 10, decodeAs[CaseList](t, w.do(t, http.MethodGet, smokePath+"/cases?runs=10", viewer, nil)).Window)
	assert.Equal(t, 30, decodeAs[CaseList](t, w.do(t, http.MethodGet, smokePath+"/cases?runs=7", viewer, nil)).Window,
		"unknown windows fall back")
}

func TestCaseHistory(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest())
	rec := w.do(t, http.MethodGet, smokePath+"/cases/history?case="+url.QueryEscape("shop › checkout"), viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeAs[CaseHistory](t, rec)
	assert.Equal(t, "shop › checkout", got.Key)
	assert.Equal(t, 1, got.Passed)
	assert.Equal(t, 1, got.Failed)
	require.Len(t, got.Runs, 2)
	assert.Equal(t, cctest.RunUID, got.Runs[0].RunID, "runs go by UID (run history)")
	assert.Equal(t, "smoke-abcde", got.Runs[0].RunName)
	assert.Equal(t, "timeout after 5s", got.Runs[0].Message)

	missing := w.do(t, http.MethodGet, smokePath+"/cases/history", viewer, nil)
	assert.Equal(t, http.StatusBadRequest, missing.Code)
	assert.Equal(t, "Which test case? (?case= is missing)", errorOf(t, missing))
}

func TestCompareRun(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest(), casesRun(), olderRun())

	pick := decodeAs[RunCaseDiff](t, w.do(t, http.MethodGet, runAPI+"smoke-abcde/compare", viewer, nil))
	assert.Equal(t, "smoke", pick.TestRef)
	assert.Nil(t, pick.Base)
	require.Len(t, pick.Candidates, 1)
	assert.Equal(t, "smoke-older", pick.Candidates[0].Name, "the other finished runs, not itself")

	rec := w.do(t, http.MethodGet, runAPI+"smoke-abcde/compare?with=smoke-older", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	d := decodeAs[RunCaseDiff](t, rec)
	require.NotNil(t, d.Base)
	assert.Equal(t, "smoke-older", d.Base.Name)
	assert.Equal(t, 5, d.RunCases)
	assert.Equal(t, 5, d.BaseCases)
	assert.Equal(t, []CaseDiff{{Key: "shop › checkout", Before: "passed", Now: "failed", DurationDiffMs: 600,
		Message: "timeout after 5s"}}, d.StartedFailing)
	assert.Equal(t, "shop › refund", d.StillFailing[0].Key)
	assert.Equal(t, "shop › search", d.Fixed[0].Key)
	assert.Equal(t, "shop › wishlist", d.Added[0].Key)
	assert.Equal(t, CaseDiff{Key: "shop › legacy", Before: "passed"}, d.Removed[0])
	assert.Equal(t, 1, d.Unchanged)

	assert.Equal(t, http.StatusNotFound, w.do(t, http.MethodGet, runAPI+"smoke-abcde/compare?with=ghost", viewer, nil).Code)
}

func TestCompareRun_NoHistory(t *testing.T) {
	// Without run history (no case store) the cases can't be read: said, not failed.
	w := newWorld(t, smokeTest(), casesRun(), olderRun())
	d := decodeAs[RunCaseDiff](t, w.do(t, http.MethodGet, runAPI+"smoke-abcde/compare?with=smoke-older", viewer, nil))
	require.NotNil(t, d.Base)
	assert.NotEmpty(t, d.CasesError)
}

func TestDiffCases(t *testing.T) {
	tc := func(key, status string) apiclient.TestCase { return apiclient.TestCase{Key: key, Status: status} }
	var d RunCaseDiff
	diffCases(&d,
		[]apiclient.TestCase{tc("a", "failed"), tc("b", "error"), tc("c", "passed"), tc("d", "skipped"), tc("new", "passed")},
		[]apiclient.TestCase{tc("a", "passed"), tc("b", "failed"), tc("c", "error"), tc("d", "skipped"), tc("gone", "failed")},
	)
	keys := func(rows []CaseDiff) (out []string) {
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}
	assert.Equal(t, []string{"a"}, keys(d.StartedFailing))
	assert.Equal(t, []string{"b"}, keys(d.StillFailing))
	assert.Equal(t, []string{"c"}, keys(d.Fixed))
	assert.Equal(t, []string{"new"}, keys(d.Added))
	assert.Equal(t, []string{"gone"}, keys(d.Removed))
	assert.Equal(t, 1, d.Unchanged)
}
