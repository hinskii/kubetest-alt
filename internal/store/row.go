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

package store

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// MetricParseWarning is emitted by RowFromRun for each non-parseable metric
// value. Callers pass a warn func to receive them (production wires it to
// controller-runtime logger; tests capture into a slice). Kept as a
// separate type so tests can assert warnings without string-matching.
type MetricParseWarning struct {
	// Key is the metric name (from ExecutionResult.Metrics via TestRun.Status).
	Key string
	// RawValue is what the CRD stored — kept for the warn log so operators
	// can see WHY it didn't parse.
	RawValue string
	// Err is the parse error from strconv.ParseFloat.
	Err error
}

func (w MetricParseWarning) Error() string {
	return fmt.Sprintf("metric %q value %q: %s", w.Key, w.RawValue, w.Err.Error())
}

// WarnFunc receives metric-parse warnings. Signature intentionally simple
// so callers can wrap logr, zap, etc. Nil is allowed — warnings are dropped
// silently in that case (fine for pure-func tests).
type WarnFunc func(MetricParseWarning)

// RowFromRun projects a terminal TestRun into a Row ready for DB insert.
//
// Metric-parsing policy (per step-09 requirement): Status.Metrics is
// map[string]string in the CRD (see step-07 decision). Values that don't
// parse as float64 are SKIPPED (dropped from the DB row) with a warning to
// warn — they never fail the whole save. Rationale: a single tool emitting
// a malformed metric line shouldn't block persisting the whole run.
//
// warn may be nil.
func RowFromRun(run *testsv1alpha1.TestRun, warn WarnFunc) (Row, error) {
	if run == nil {
		return Row{}, fmt.Errorf("store: nil TestRun")
	}
	if run.UID == "" {
		return Row{}, fmt.Errorf("store: TestRun %s/%s has empty UID", run.Namespace, run.Name)
	}
	if run.Status.FinishedAt == nil {
		return Row{}, fmt.Errorf("store: TestRun %s/%s has no FinishedAt", run.Namespace, run.Name)
	}

	r := Row{
		UID:        string(run.UID),
		Name:       run.Name,
		Namespace:  run.Namespace,
		TestRef:    run.Spec.TestRef,
		Phase:      string(run.Status.Phase),
		Source:     run.Spec.Source,
		FinishedAt: run.Status.FinishedAt.UTC(),
		DurationMs: run.Status.DurationMs,
		LogsRef:    run.Status.LogsRef,
		Message:    run.Status.Message,
		Tags:       cloneStringMap(run.Spec.Tags),
	}
	if run.Status.QueuedAt != nil {
		t := run.Status.QueuedAt.UTC()
		r.QueuedAt = &t
	}
	if run.Status.StartedAt != nil {
		t := run.Status.StartedAt.UTC()
		r.StartedAt = &t
	}
	if run.Status.TestCounts != nil {
		r.TestCounts = &TestCounts{
			Total:   run.Status.TestCounts.Total,
			Passed:  run.Status.TestCounts.Passed,
			Failed:  run.Status.TestCounts.Failed,
			Skipped: run.Status.TestCounts.Skipped,
		}
	}
	if len(run.Status.ArtifactRefs) > 0 {
		r.ArtifactRefs = make([]ArtifactRef, len(run.Status.ArtifactRefs))
		for i, a := range run.Status.ArtifactRefs {
			r.ArtifactRefs[i] = ArtifactRef{
				Path:        a.Path,
				Key:         a.Key,
				SizeBytes:   a.SizeBytes,
				ContentType: a.ContentType,
			}
		}
	}
	if len(run.Status.Steps) > 0 {
		steps := make(map[string]any, len(run.Status.Steps))
		for name, s := range run.Status.Steps {
			m := map[string]any{"phase": string(s.Phase)}
			if s.QueuedAt != nil {
				m["queuedAt"] = s.QueuedAt.UTC()
			}
			if s.StartedAt != nil {
				m["startedAt"] = s.StartedAt.UTC()
			}
			if s.FinishedAt != nil {
				m["finishedAt"] = s.FinishedAt.UTC()
			}
			steps[name] = m
		}
		r.Steps = steps
	}
	if run.Status.ResolvedSpec != "" {
		// ResolvedSpec is a JSON snapshot of Test.Spec captured at start.
		// Round-trip through generic map so it lands in a JSONB column
		// without the DB caring about the CRD schema.
		var spec map[string]any
		if err := json.Unmarshal([]byte(run.Status.ResolvedSpec), &spec); err == nil {
			r.ResolvedSpec = spec
		}
		// Unparseable snapshot is silently ignored — the raw string still
		// lives on the CRD for debugging; polluting the DB row with a raw
		// string in a JSONB column would break the schema.
	}
	if len(run.Status.Metrics) > 0 {
		r.Metrics = parseMetrics(run.Status.Metrics, warn)
	}
	return r, nil
}

// parseMetrics converts the CRD's string-typed metrics map into float64s,
// skipping (with a warning) any value that doesn't parse. Returns nil if
// nothing survives — callers store NULL rather than an empty JSONB object.
func parseMetrics(in map[string]string, warn WarnFunc) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			if warn != nil {
				warn(MetricParseWarning{Key: k, RawValue: v, Err: err})
			}
			continue
		}
		out[k] = f
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cloneStringMap returns a nil-safe copy so subsequent CRD mutations don't
// alias into the stored row.
func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}

// IsTerminal returns true for phases that RowFromRun accepts. Kept public
// so the controller can gate SaveFinished on it without importing the
// CRD's phase set separately.
func IsTerminal(phase testsv1alpha1.Phase) bool {
	switch phase {
	case testsv1alpha1.PhasePassed,
		testsv1alpha1.PhaseFailed,
		testsv1alpha1.PhaseAborted,
		testsv1alpha1.PhaseError:
		return true
	default:
		return false
	}
}
