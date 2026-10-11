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

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// What /entry does after the tool, said in the log: the run's "collect
// results" stage in Control Center (step 20a) — how the tool ended, where
// the verdict came from, metrics, artifacts, the result.

// say writes one line of the collect log — to stdout, like the marker
// before it: the kubelet reads stdout and stderr separately, so lines on
// stderr could land on either side of the marker.
func (e *Entry) say(format string, args ...any) {
	w := e.Stdout
	if w == nil {
		w = e.Loader
	}
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}

// sinceRound is how long ago t was, rounded for people.
func sinceRound(t time.Time) string { return took(time.Since(t)) }

// took is d for people: 350ms, 2.1s, 1m30.5s.
func took(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

// sayTool: how the tool ended — its exit code and time, the tries.
func (e *Entry) sayTool(ctx context.Context, req ExecutionRequest, r ExecutionResult, d time.Duration) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		e.say("Tool stopped: the timeout (%ds) ran out after %s", req.TimeoutSeconds, took(d))
	case errors.Is(ctx.Err(), context.Canceled):
		e.say("Tool stopped: the run was aborted after %s", took(d))
	case r.ToolExitCode == nil:
		e.say("Tool did not start: %s", r.ErrorMessage)
	default:
		e.say("Tool exited with code %d after %s", *r.ToolExitCode, took(d))
	}
	if n := len(r.Attempts); n > 1 {
		phases := make([]string, 0, n)
		for _, a := range r.Attempts {
			phases = append(phases, a.Phase)
		}
		e.say("Tries: %d (%s)", n, strings.Join(phases, ", "))
	}
}

// sayVerdict: the verdict and where it came from.
func (e *Entry) sayVerdict(req ExecutionRequest, r ExecutionResult) {
	from := "the exit code"
	switch req.Verdict.From {
	case "junit":
		from = "the JUnit report (the exit code doesn't count)"
	case "jtl":
		from = "the JMeter results (error rate at most " + orDefault(req.Verdict.ErrorRateMax, "0") + ")"
	}
	line := fmt.Sprintf("Verdict: %s, from %s", r.Phase, from)
	if r.ErrorMessage != "" {
		line += " — " + r.ErrorMessage
	}
	e.say("%s", line)
}

// sayArtifacts: what the scrape found and stored.
func (e *Entry) sayArtifacts(req ExecutionRequest, r ExecutionResult) {
	switch {
	case e.Scraper == nil:
		e.say("Artifacts: not kept (no object storage)")
		return
	case len(req.Artifacts.Paths) == 0:
		e.say("Artifacts: none asked for (spec.artifacts.paths is empty)")
	case len(r.Artifacts) == 0:
		e.say("Artifacts: nothing matched %s", strings.Join(req.Artifacts.Paths, ", "))
	default:
		var size int64
		for _, a := range r.Artifacts {
			size += a.SizeBytes
		}
		e.say("Artifacts: %d %s (%s) matched %s — uploaded", len(r.Artifacts), pluralWord(len(r.Artifacts), "file"),
			humanBytes(size), strings.Join(req.Artifacts.Paths, ", "))
		for i, a := range r.Artifacts {
			if i == 10 {
				e.say("  … and %d more", len(r.Artifacts)-10)
				break
			}
			e.say("  %s (%s)", a.Path, humanBytes(a.SizeBytes))
		}
	}
	if tc := r.TestCounts; tc != nil {
		e.say("Tests: %d (%d passed, %d failed, %d skipped)", tc.Total, tc.Passed, tc.Failed, tc.Skipped)
	}
	if len(r.TestCases) > 0 && r.TestCounts != nil {
		e.say("Test cases: %d from JUnit reports, kept in run history", len(r.TestCases))
	}
	if r.ScrapeError != "" {
		e.say("Artifacts: %s", r.ScrapeError)
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func pluralWord(n int, one string) string {
	if n == 1 {
		return one
	}
	return one + "s"
}

// humanBytes is n bytes for people: 14 B, 1.2 kB, 3.4 MB.
func humanBytes(n int64) string {
	const k = 1000
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.1f kB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
}
