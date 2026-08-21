/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package composer holds pure step-17 logic: aggregate per-step child
// phases into a step verdict, resolve the execute graph with cycle +
// depth guards, and render per-child config with {{ index }} /
// {{ count }} scope. Zero k8s deps — controller wires this into the
// TestRun reconciler.
package composer

import (
	"errors"
	"fmt"
	"strings"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// ConditionAlways is the step.condition value that opts a step into
// running regardless of prior-step outcome (mirrors TestWorkflow).
// Kept as a const so goconst stays quiet and typo-drift can't
// silently disable skip-on-fail.
const ConditionAlways = "always"

// ChildOutcome is what the reconciler feeds Aggregate for one child
// TestRun that participates in a step. Kept struct-shaped (not a bare
// Phase) so future fields (retries used, timeoutAborted) can be added
// without breaking every caller.
type ChildOutcome struct {
	// Phase MUST be terminal (passed / failed / aborted / error) —
	// Aggregate does not itself gate on non-terminal children; callers
	// must not invoke it until every child has landed.
	Phase testsv1alpha1.Phase
}

// StepVerdict is Aggregate's output.
type StepVerdict struct {
	// Phase is the step-level verdict: passed / failed / aborted /
	// error. `skipped` is NOT produced here — that's the reconciler's
	// job when a prior non-optional step failed (see MarkSkipped).
	Phase testsv1alpha1.Phase

	// Message is a short human-readable summary (fed into
	// TestRunStatus.Steps[key].Message-like fields in future; for now
	// surfaced via the step-aggregate StepResult if we grow one).
	Message string
}

// AggregateOpts folds per-step spec knobs (negative, optional) into
// Aggregate so callers don't need to pass a whole api Step. Optional
// is NOT read here — it's a PARENT-level concern (whether this step's
// failure propagates to the parent); Aggregate returns the step's
// own verdict either way.
type AggregateOpts struct {
	// Negative inverts per-child pass/fail before aggregation:
	// passed → treated as failed, failed → treated as passed, other
	// terminal phases (aborted, error) pass through as-is (a genuine
	// infra crash is not something "expected to fail" should mask).
	Negative bool
}

// Aggregate collapses N child outcomes into a step verdict.
//
// Rules (mirrors CLAUDE.md's plan-17 line "step passed iff all
// passed"):
//   - empty children → failed (this shouldn't happen because the
//     webhook rejects execute.tests=[], but Aggregate stays
//     defensive: a step that started N children and receives 0
//     terminal outcomes is genuinely broken).
//   - any child aborted → step aborted (user/system cancel wins).
//   - any child error → step error (infra beats verdict).
//   - any child failed → step failed.
//   - all children passed → step passed.
//
// Negative inverts pass↔fail per child BEFORE the fold — an
// aborted or error child still short-circuits regardless.
func Aggregate(children []ChildOutcome, opts AggregateOpts) StepVerdict {
	if len(children) == 0 {
		return StepVerdict{Phase: testsv1alpha1.PhaseFailed, Message: "aggregate: no child outcomes recorded"}
	}
	var haveFailed bool
	failedIdx := -1
	for i, c := range children {
		phase := c.Phase
		// Aborted / error short-circuit — never inverted by Negative.
		if phase == testsv1alpha1.PhaseAborted {
			return StepVerdict{Phase: testsv1alpha1.PhaseAborted,
				Message: fmt.Sprintf("aggregate: child[%d] aborted", i)}
		}
		if phase == testsv1alpha1.PhaseError {
			return StepVerdict{Phase: testsv1alpha1.PhaseError,
				Message: fmt.Sprintf("aggregate: child[%d] error", i)}
		}
		if opts.Negative {
			switch phase {
			case testsv1alpha1.PhasePassed:
				phase = testsv1alpha1.PhaseFailed
			case testsv1alpha1.PhaseFailed:
				phase = testsv1alpha1.PhasePassed
			}
		}
		if phase == testsv1alpha1.PhaseFailed && !haveFailed {
			haveFailed = true
			failedIdx = i
		}
	}
	if haveFailed {
		msg := fmt.Sprintf("aggregate: %d/%d children failed (first: index %d)", countPhase(children, opts, testsv1alpha1.PhaseFailed), len(children), failedIdx)
		return StepVerdict{Phase: testsv1alpha1.PhaseFailed, Message: msg}
	}
	return StepVerdict{Phase: testsv1alpha1.PhasePassed,
		Message: fmt.Sprintf("aggregate: %d/%d children passed", len(children), len(children))}
}

func countPhase(children []ChildOutcome, opts AggregateOpts, want testsv1alpha1.Phase) int {
	n := 0
	for _, c := range children {
		phase := c.Phase
		if opts.Negative {
			switch phase {
			case testsv1alpha1.PhasePassed:
				phase = testsv1alpha1.PhaseFailed
			case testsv1alpha1.PhaseFailed:
				phase = testsv1alpha1.PhasePassed
			}
		}
		if phase == want {
			n++
		}
	}
	return n
}

// ShouldRunNext reports whether the NEXT step should run given the
// aggregate verdict of the just-completed step, the next step's
// declared condition, and whether the completed step was optional.
//
// Rules:
//   - condition "always" → true (unconditional).
//   - condition "" or "passed" → prior step must have passed OR must
//     be optional (optional failures don't gate follow-up "passed"
//     steps — a smoke test that's allowed to fail shouldn't block the
//     load test).
func ShouldRunNext(priorVerdict StepVerdict, priorOptional bool, nextCondition string) bool {
	if nextCondition == ConditionAlways {
		return true
	}
	// aborted / error also block subsequent condition:passed steps —
	// there's no world where "wait, retry the load test after infra
	// died" is what a user wants without an explicit `always`.
	if priorVerdict.Phase == testsv1alpha1.PhasePassed {
		return true
	}
	if priorOptional && priorVerdict.Phase == testsv1alpha1.PhaseFailed {
		return true
	}
	return false
}

// ParentPhase folds the ordered step verdicts + optional flags into
// the parent TestRun's terminal phase. Only called after ALL steps
// have either run (verdict set) or been skipped (nil verdict, skip
// marker in the parallel `skipped` slice).
//
//   - any non-optional step aborted → parent aborted.
//   - any non-optional step error → parent error.
//   - any non-optional step failed → parent failed.
//   - all non-optional steps passed (or all skipped after a failure
//     that got recorded above) → parent passed.
//
// Skipped steps do NOT contribute to the fold — they mark "we chose
// not to run this because of skip-on-fail" and the earlier failure
// already drove parent=failed.
func ParentPhase(stepVerdicts []*StepVerdict, optional []bool) testsv1alpha1.Phase {
	// First pass: severe short-circuits (aborted/error > failed).
	for i, v := range stepVerdicts {
		if v == nil {
			continue // skipped
		}
		if optional != nil && i < len(optional) && optional[i] {
			continue
		}
		if v.Phase == testsv1alpha1.PhaseAborted {
			return testsv1alpha1.PhaseAborted
		}
		if v.Phase == testsv1alpha1.PhaseError {
			return testsv1alpha1.PhaseError
		}
	}
	for i, v := range stepVerdicts {
		if v == nil {
			continue
		}
		if optional != nil && i < len(optional) && optional[i] {
			continue
		}
		if v.Phase == testsv1alpha1.PhaseFailed {
			return testsv1alpha1.PhaseFailed
		}
	}
	return testsv1alpha1.PhasePassed
}

// ErrCycle is returned by ResolveGraph when the execute graph has a
// cycle. The reconciler surfaces it as phase=error with the human-
// readable path (a → b → a).
var ErrCycle = errors.New("composition cycle")

// ErrDepthLimit is returned when the execute graph nests deeper than
// the plan's depth limit (10). Deep nesting is almost always a bug;
// a real user with 20 layers of composition should split their tree.
var ErrDepthLimit = errors.New("composition depth limit exceeded")

// MaxDepth is the plan's fixed limit — kept as a package const so
// tests can reference it directly without duplicating the number.
const MaxDepth = 10

// TestLookup returns a Test's Spec for the given name, or (nil, ok=false)
// when the named Test does not exist in the same namespace. Kept as
// a func rather than a map so the reconciler can wire a client-backed
// lookup without changing the composer signature.
type TestLookup func(name string) (*testsv1alpha1.TestSpec, bool)

// ResolveGraph walks a composite Test's execute graph rooted at
// rootSpec, following each StepExecuteTest.Name via lookup, and
// returns nil on success or an error naming the cycle path / missing
// dep / depth overrun.
//
// Missing children are NOT errors here — the reconciler surfaces
// them at step execution time so `optional` can honor them. Only
// cycles + depth are structural bugs the graph resolver catches.
func ResolveGraph(rootName string, rootSpec *testsv1alpha1.TestSpec, lookup TestLookup) error {
	visiting := map[string]bool{}
	path := []string{}
	var walk func(name string, spec *testsv1alpha1.TestSpec, depth int) error
	walk = func(name string, spec *testsv1alpha1.TestSpec, depth int) error {
		if depth > MaxDepth {
			return fmt.Errorf("%w: %s reached depth %d", ErrDepthLimit, name, depth)
		}
		if visiting[name] {
			// Cycle. Build a a→b→...→a-style path.
			cyclePath := append(append([]string{}, path...), name)
			return fmt.Errorf("%w: %s", ErrCycle, joinPath(cyclePath))
		}
		if len(spec.Steps) == 0 {
			return nil // leaf — nothing to descend into.
		}
		visiting[name] = true
		path = append(path, name)
		defer func() {
			delete(visiting, name)
			path = path[:len(path)-1]
		}()
		for _, s := range spec.Steps {
			if s.Execute == nil {
				continue
			}
			for _, t := range s.Execute.Tests {
				child, ok := lookup(t.Name)
				if !ok {
					continue // missing — controller handles at exec time.
				}
				if err := walk(t.Name, child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(rootName, rootSpec, 0)
}

func joinPath(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(names[0])
	for _, n := range names[1:] {
		b.WriteString(" → ")
		b.WriteString(n)
	}
	return b.String()
}
