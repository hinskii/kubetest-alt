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
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// How Control Center reads kubetest objects for people: shared by the
// API and the server-rendered pages that remain (step 20R).

// Labels Control Center reads from Tests.
const (
	LabelTool      = "kubetest.io/tool"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedByUI marks a Test the GUI may edit (CLAUDE.md §7).
	ManagedByUI = "ui"
	// TagCreatedBy names who started a run (a TestRun tag).
	TagCreatedBy = "kubetest.io/created-by"
	// conditionReady is a Test's readiness (the operator's Test
	// controller): False when it can't run as it is.
	conditionReady = "Ready"
)

// NotReady is the message of a Test's Ready=False condition, or "".
func NotReady(conds []metav1.Condition) string {
	if c := meta.FindStatusCondition(conds, conditionReady); c != nil && c.Status == metav1.ConditionFalse {
		return cmp.Or(c.Message, c.Reason)
	}
	return ""
}

// Locked: the Test is managed outside the GUI (GitOps, kubectl) —
// read-only here, runs allowed (CLAUDE.md §7).
func Locked(labels map[string]string) bool { return labels[LabelManagedBy] != ManagedByUI }

// ParamsOf lists a Test's spec.config parameters by name.
func ParamsOf(spec *testsv1alpha1.TestSpec) []Param {
	if spec == nil {
		return []Param{}
	}
	out := make([]Param, 0, len(spec.Config))
	for _, name := range slices.Sorted(maps.Keys(spec.Config)) {
		p := spec.Config[name]
		out = append(out, Param{Name: name, Type: p.Type, Default: p.Default, Pattern: p.Pattern,
			Enum: p.Enum, Required: p.Default == "", Description: p.Description})
	}
	return out
}

// cronParser is the operator's and the webhook's parser: five fields, an
// optional CRON_TZ=<zone> prefix, UTC otherwise.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// NextFiresShown is how many upcoming fire times a schedule lists.
const NextFiresShown = 3

// NextFires returns the next n fire times of a cron expression after now,
// in UTC.
func NextFires(expr string, now time.Time, n int) ([]time.Time, error) {
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

// ScheduleOf is a Test's recurring schedule, nil without one.
func ScheduleOf(expr string, now time.Time) *Schedule {
	if expr == "" {
		return nil
	}
	s := &Schedule{Expr: expr}
	next, err := NextFires(expr, now, NextFiresShown)
	if err != nil {
		s.Error = err.Error()
	}
	s.Next = next
	if s.Next == nil {
		s.Next = []time.Time{}
	}
	return s
}

// GeneratePrefix is a TestRun's metadata.generateName for a Test: the
// name, cut so the generated name fits.
func GeneratePrefix(test string) string {
	if len(test) > 50 {
		test = strings.TrimRight(test[:50], "-.")
	}
	return test + "-"
}

// RunID is the id the API finds a run by: its name while the TestRun
// exists, its UID once only run history has it (the API looks archived
// runs up by UID).
func RunID(r apiclient.Run) string {
	if r.Origin == apiclient.OriginArchive && r.UID != "" {
		return r.UID
	}
	return r.Name
}

// CommitURL links a commit on GitHub, GitLab or Bitbucket (cloud or a
// self-hosted instance with the name in its host), from an https, ssh://
// or scp-style (git@host:owner/repo.git) repository URI. "" for anything
// else — the commit is then shown without a link.
func CommitURL(repo, sha string) string {
	if sha == "" {
		return ""
	}
	var host, path string
	if u, err := url.Parse(repo); err == nil && u.Host != "" {
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else if at, colon := strings.Index(repo, "@"), strings.Index(repo, ":"); at >= 0 && colon > at {
		host, path = repo[at+1:colon], repo[colon+1:]
	} else {
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || strings.Count(path, "/") < 1 {
		return ""
	}
	base := "https://" + host + "/" + path
	switch h := strings.ToLower(host); {
	case strings.Contains(h, "github"):
		return base + "/commit/" + url.PathEscape(sha)
	case strings.Contains(h, "gitlab"):
		return base + "/-/commit/" + url.PathEscape(sha)
	case strings.Contains(h, "bitbucket"):
		return base + "/commits/" + url.PathEscape(sha)
	}
	return ""
}

// NaturalCompare orders strings with numbers by value: "worker-2" before
// "worker-10".
func NaturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitsPrefix(a), digitsPrefix(b)
		if da != "" && db != "" {
			if len(da) != len(db) {
				return len(da) - len(db)
			}
			if c := strings.Compare(da, db); c != 0 {
				return c
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digitsPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
