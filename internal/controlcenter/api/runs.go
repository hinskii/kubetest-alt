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
	"fmt"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// RunsPerPage is the history page size of a Test's runs.
const RunsPerPage = 25

// RunPage is one page of a Test's runs: live runs (first page only),
// then finished runs, newest first.
type RunPage struct {
	Runs []RunRow `json:"runs" required:"true" nullable:"false"`
	// Next is the cursor of the next (older) page; absent on the last.
	Next string `json:"next,omitempty"`
}

// RunRow is a run as a history table shows it.
type RunRow struct {
	// ID finds the run (its name, or its UID once only history has it).
	ID    string `json:"id" required:"true"`
	Name  string `json:"name" required:"true"`
	Phase string `json:"phase" required:"true"`
	// StartedAt, or NotBefore while it waits for its start time.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	NotBefore  *time.Time `json:"notBefore,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	Commit     *Commit    `json:"commit,omitempty"`
	Source     string     `json:"source,omitempty"`
	CreatedBy  string     `json:"createdBy,omitempty"`
	Comment    string     `json:"comment,omitempty"`
}

// Commit is the git commit a run checked out.
type Commit struct {
	SHA string `json:"sha" required:"true"`
	// URL links it on the git host, when known.
	URL string `json:"url,omitempty"`
}

// RunRequest starts a run of a Test.
type RunRequest struct {
	// Config sets parameters; values equal to the default are left to it.
	Config map[string]string `json:"config,omitempty"`
	// NotBefore delays the start (a one-shot schedule).
	NotBefore *time.Time `json:"notBefore,omitempty"`
}

// StartedRun is the run that was created.
type StartedRun struct {
	Name      string     `json:"name" required:"true"`
	Namespace string     `json:"namespace" required:"true"`
	NotBefore *time.Time `json:"notBefore,omitempty"`
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *Server) listTestRuns(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	page, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{
		Namespace: r.PathValue("ns"), Test: r.PathValue("name"), Limit: RunsPerPage, After: r.URL.Query().Get("after"),
	})
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := RunPage{Runs: make([]RunRow, 0, len(page.Runs)), Next: page.NextCursor}
	for _, run := range page.Runs {
		row := RunRow{ID: RunID(run), Name: run.Name, Phase: run.Phase, StartedAt: utc(run.StartedAt),
			NotBefore: utc(run.NotBefore), DurationMs: run.DurationMs, Source: run.Source,
			CreatedBy: run.Tags[TagCreatedBy]}
		if run.Git != nil && run.Git.Commit != "" {
			row.Commit = &Commit{SHA: run.Git.Commit, URL: CommitURL(run.Git.URI, run.Git.Commit)}
		}
		if run.Comment != nil {
			row.Comment = run.Comment.Text
		}
		out.Runs = append(out.Runs, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// startRun creates a TestRun: parameters that differ from their
// defaults, and an optional start time.
func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	var in RunRequest
	if !decode(w, r, &in) {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	t, err := client.GetResolvedTest(r.Context(), ns, name)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	params := ParamsOf(t.Spec)
	known := map[string]bool{}
	overrides := map[string]string{}
	for _, p := range params {
		known[p.Name] = true
		v := strings.TrimSpace(in.Config[p.Name])
		if v == "" && p.Required {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Parameter %s is required.", p.Name))
			return
		}
		if v != "" && v != p.Default {
			overrides[p.Name] = v
		}
	}
	for k := range in.Config {
		if !known[k] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("The Test has no parameter %s.", k))
			return
		}
	}
	run := &testsv1alpha1.TestRun{Spec: testsv1alpha1.TestRunSpec{TestRef: name, Config: overrides}}
	run.GenerateName = GeneratePrefix(name)
	if in.NotBefore != nil {
		nb := metav1.NewTime(in.NotBefore.UTC())
		run.Spec.NotBefore = &nb
	}
	created, err := client.CreateRun(r.Context(), ns, run)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	writeJSON(w, http.StatusCreated, StartedRun{Name: created.Name, Namespace: ns, NotBefore: utc(in.NotBefore)})
}
