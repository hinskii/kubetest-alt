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
	"net/http"
	"strings"
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

const baseUID = cctest.BaseUID

func failedRun() *testsv1alpha1.TestRun {
	r := runOf("smoke-abcde", testsv1alpha1.PhaseFailed)
	r.Status.TestCounts = &testsv1alpha1.TestCounts{Total: 5, Passed: 3, Failed: 2}
	return r
}

func olderRun() *testsv1alpha1.TestRun {
	r := runOf("smoke-older", testsv1alpha1.PhaseFailed)
	r.UID = types.UID(baseUID)
	earlier := metav1.NewTime(time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC))
	r.Status.FinishedAt = &earlier
	return r
}

func TestCasesPage_FlakyAndSorting(t *testing.T) {
	w := newWorld(t, smokeTest())
	rec := w.get(t, "/clusters/dev/tests/team-a/smoke/cases", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "<strong>2</strong> failing in the latest run")
	assert.Contains(t, body, "<strong>1</strong> flaky", "the API marks checkout (passed and failed)")
	assert.Contains(t, body, `href="/clusters/dev/tests/team-a/smoke/cases/history?case=shop%20%e2%80%ba%20refund"`)
	assert.Contains(t, body, ">60%<", "checkout: 6 of 10")
	assert.Less(t, strings.Index(body, ">refund<"), strings.Index(body, ">checkout<"), "most failures first")

	byFlips := w.get(t, "/clusters/dev/tests/team-a/smoke/cases?sort=flaky&runs=10", "").Body.String()
	assert.Less(t, strings.Index(byFlips, ">checkout<"), strings.Index(byFlips, ">refund<"))
	assert.Contains(t, byFlips, `<option value="10" selected>`)
}

func TestCaseHistoryPage(t *testing.T) {
	w := newWorld(t, smokeTest())
	rec := w.get(t, "/clusters/dev/tests/team-a/smoke/cases/history?case="+"shop%20%E2%80%BA%20checkout", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<h1>shop › checkout</h1>")
	assert.Contains(t, body, "2 runs · 1 passed · 1 failed")
	assert.Equal(t, 2, strings.Count(body, `class="dot status-`))
	assert.Equal(t, http.StatusBadRequest, w.get(t, "/clusters/dev/tests/team-a/smoke/cases/history", "").Code)
}

func TestComparePage(t *testing.T) {
	w := newWorld(t, smokeTest(), failedRun(), olderRun())
	pick := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/compare", "").Body.String()
	assert.Contains(t, pick, `<option value="smoke-older"`)

	rec := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/compare?with=smoke-older", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	section := func(title string) string {
		i := strings.Index(body, "<h2>"+title)
		require.GreaterOrEqual(t, i, 0, title)
		rest := body[i:]
		if j := strings.Index(rest[4:], "<h2>"); j >= 0 {
			return rest[:j+4]
		}
		return rest
	}
	assert.Contains(t, section("Started failing"), "shop › checkout")
	assert.Contains(t, section("Started failing"), "&#43;600ms")
	assert.Contains(t, section("Still failing"), "shop › refund")
	assert.Contains(t, section("Fixed"), "shop › search")
	assert.Contains(t, section("New tests"), "shop › wishlist")
	assert.Contains(t, section("No longer run"), "shop › legacy")
	assert.Contains(t, body, "Unchanged: 1")
}

func TestDiffCases(t *testing.T) {
	tc := func(key, status string) apiclient.TestCase { return apiclient.TestCase{Key: key, Status: status} }
	d := diffCases(
		[]apiclient.TestCase{tc("a", "failed"), tc("b", "error"), tc("c", "passed"), tc("d", "skipped"), tc("new", "passed")},
		[]apiclient.TestCase{tc("a", "passed"), tc("b", "failed"), tc("c", "error"), tc("d", "skipped"), tc("gone", "failed")},
	)
	keys := func(rows []caseDiff) (out []string) {
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
