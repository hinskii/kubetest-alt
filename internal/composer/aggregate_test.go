/*
Copyright 2026.
*/

package composer

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestAggregate_Table covers the plan's aggregation table + a few of
// the CLAUDE.md §15.2 corner cases (aborted/error precedence). Kept
// as a plain unit test — the reconciler wiring lives in envtest.
func TestAggregate_Table(t *testing.T) {
	pass := testsv1alpha1.PhasePassed
	fail := testsv1alpha1.PhaseFailed
	abort := testsv1alpha1.PhaseAborted
	pErr := testsv1alpha1.PhaseError

	cases := []struct {
		name     string
		children []ChildOutcome
		opts     AggregateOpts
		want     testsv1alpha1.Phase
	}{
		{"all pass", outcomes(pass, pass, pass), AggregateOpts{}, pass},
		{"one fails", outcomes(pass, fail, pass), AggregateOpts{}, fail},
		{"all fail", outcomes(fail, fail, fail), AggregateOpts{}, fail},
		{"aborted wins over failed", outcomes(fail, abort, pass), AggregateOpts{}, abort},
		{"error wins over failed", outcomes(fail, pErr, pass), AggregateOpts{}, pErr},
		{"aborted wins over error", outcomes(abort, pErr, fail), AggregateOpts{}, abort},
		{"negative: fail-only counts as pass", outcomes(fail, fail), AggregateOpts{Negative: true}, pass},
		{"negative: pass-only counts as fail", outcomes(pass, pass), AggregateOpts{Negative: true}, fail},
		{"negative doesn't swallow abort", outcomes(pass, abort), AggregateOpts{Negative: true}, abort},
		{"negative doesn't swallow error", outcomes(pass, pErr), AggregateOpts{Negative: true}, pErr},
		{"empty children → failed defensively", nil, AggregateOpts{}, fail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Aggregate(tc.children, tc.opts)
			assert.Equal(t, tc.want, got.Phase, "verdict message: %s", got.Message)
		})
	}
}

// TestAggregate_InvariantNegation exists because the README's
// "invariant-negation check" convention says: for every invariant
// asserted positively, negate a single input and assert the invariant
// FAILS. This proves the assertion isn't a tautology.
//
// Invariant under test: "step passed iff EVERY non-aborted / non-error
// child passed" (after Negative inversion). Positive: all-passed →
// passed. Negation: flip one child to failed and re-run — MUST NOT
// be passed. Same for the negative-inverted invariant.
func TestAggregate_InvariantNegation(t *testing.T) {
	pass := testsv1alpha1.PhasePassed
	fail := testsv1alpha1.PhaseFailed

	// Positive: 4 passes → passed.
	all := outcomes(pass, pass, pass, pass)
	assert.Equal(t, testsv1alpha1.PhasePassed, Aggregate(all, AggregateOpts{}).Phase)

	// Negation: flip child 2 to failed → MUST become failed.
	flipped := outcomes(pass, pass, fail, pass)
	assert.NotEqual(t, testsv1alpha1.PhasePassed, Aggregate(flipped, AggregateOpts{}).Phase,
		"Aggregate must reject an all-passed verdict when one child failed — otherwise the assertion is a tautology")
	assert.Equal(t, testsv1alpha1.PhaseFailed, Aggregate(flipped, AggregateOpts{}).Phase)

	// Same invariant, Negative branch: all-failed under Negative → passed;
	// flip one to passed → must become failed.
	allFailedNeg := outcomes(fail, fail, fail)
	assert.Equal(t, testsv1alpha1.PhasePassed, Aggregate(allFailedNeg, AggregateOpts{Negative: true}).Phase)
	flippedNeg := outcomes(fail, pass, fail)
	assert.NotEqual(t, testsv1alpha1.PhasePassed, Aggregate(flippedNeg, AggregateOpts{Negative: true}).Phase,
		"Negative Aggregate must reject all-passed-under-inversion when a real pass sneaks in")
}

// TestShouldRunNext covers the plan's "condition: passed" + "always"
// + optional-fail-doesn't-block matrix.
func TestShouldRunNext(t *testing.T) {
	pass := StepVerdict{Phase: testsv1alpha1.PhasePassed}
	fail := StepVerdict{Phase: testsv1alpha1.PhaseFailed}
	abort := StepVerdict{Phase: testsv1alpha1.PhaseAborted}

	cases := []struct {
		name          string
		prior         StepVerdict
		priorOptional bool
		nextCond      string
		want          bool
	}{
		{"passed + passed cond → run", pass, false, "passed", true},
		{"passed + empty cond → run (empty defaults passed)", pass, false, "", true},
		{"failed + passed cond → skip", fail, false, "passed", false},
		{"failed-optional + passed cond → run (optional doesn't block)", fail, true, "passed", true},
		{"aborted + passed cond → skip", abort, false, "passed", false},
		{"aborted-optional + passed cond → skip (aborted isn't optional-forgiven)", abort, true, "passed", false},
		{"failed + always cond → run", fail, false, "always", true},
		{"aborted + always cond → run", abort, false, "always", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShouldRunNext(tc.prior, tc.priorOptional, tc.nextCond))
		})
	}
}

// TestParentPhase folds ordered step verdicts + optional flags into a
// terminal parent phase.
func TestParentPhase(t *testing.T) {
	pass := &StepVerdict{Phase: testsv1alpha1.PhasePassed}
	fail := &StepVerdict{Phase: testsv1alpha1.PhaseFailed}
	abort := &StepVerdict{Phase: testsv1alpha1.PhaseAborted}
	pErr := &StepVerdict{Phase: testsv1alpha1.PhaseError}

	cases := []struct {
		name     string
		verdicts []*StepVerdict
		optional []bool
		want     testsv1alpha1.Phase
	}{
		{"all pass → passed", []*StepVerdict{pass, pass}, nil, testsv1alpha1.PhasePassed},
		{"one fail → failed", []*StepVerdict{pass, fail, pass}, nil, testsv1alpha1.PhaseFailed},
		{"one fail-optional excluded → passed", []*StepVerdict{pass, fail}, []bool{false, true}, testsv1alpha1.PhasePassed},
		{"abort beats failed", []*StepVerdict{fail, abort}, nil, testsv1alpha1.PhaseAborted},
		{"error beats failed", []*StepVerdict{fail, pErr}, nil, testsv1alpha1.PhaseError},
		{"skipped nil excluded", []*StepVerdict{pass, nil, nil}, nil, testsv1alpha1.PhasePassed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ParentPhase(tc.verdicts, tc.optional))
		})
	}
}

// TestResolveGraph_Cycle: A → B → A must error with ErrCycle and name
// both nodes.
func TestResolveGraph_Cycle(t *testing.T) {
	specs := map[string]*testsv1alpha1.TestSpec{
		"a": compositeSpec("b"),
		"b": compositeSpec("a"),
	}
	err := ResolveGraph("a", specs["a"], mapLookup(specs))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCycle), "want ErrCycle, got %v", err)
	assert.Contains(t, err.Error(), "a → b → a")
}

// TestResolveGraph_Depth: nested composition beyond MaxDepth errors.
func TestResolveGraph_Depth(t *testing.T) {
	specs := map[string]*testsv1alpha1.TestSpec{}
	for i := 0; i <= MaxDepth+1; i++ {
		next := ""
		if i < MaxDepth+1 {
			next = nameAt(i + 1)
		}
		if next != "" {
			specs[nameAt(i)] = compositeSpec(next)
		} else {
			specs[nameAt(i)] = &testsv1alpha1.TestSpec{Container: testsv1alpha1.ContainerConfig{Image: "x", Command: []string{"y"}}}
		}
	}
	err := ResolveGraph(nameAt(0), specs[nameAt(0)], mapLookup(specs))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDepthLimit))
}

// TestResolveGraph_Missing: a step referencing a Test that doesn't
// exist is NOT a graph-time error — the reconciler handles this so
// step.optional can honor missing deps.
func TestResolveGraph_Missing(t *testing.T) {
	specs := map[string]*testsv1alpha1.TestSpec{
		"a": compositeSpec("does-not-exist"),
	}
	assert.NoError(t, ResolveGraph("a", specs["a"], mapLookup(specs)))
}

// TestResolveGraph_DAG: multiple children pointing at the same leaf
// resolves cleanly (visiting is reset on the way up).
func TestResolveGraph_DAG(t *testing.T) {
	leaf := &testsv1alpha1.TestSpec{Container: testsv1alpha1.ContainerConfig{Image: "x", Command: []string{"y"}}}
	specs := map[string]*testsv1alpha1.TestSpec{
		"root": {Steps: []testsv1alpha1.Step{{Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{
			{Name: "leaf"}, {Name: "leaf"},
		}}}}},
		"leaf": leaf,
	}
	assert.NoError(t, ResolveGraph("root", specs["root"], mapLookup(specs)))
}

// ----- helpers -----

func outcomes(phases ...testsv1alpha1.Phase) []ChildOutcome {
	out := make([]ChildOutcome, len(phases))
	for i, p := range phases {
		out[i] = ChildOutcome{Phase: p}
	}
	return out
}

func compositeSpec(child string) *testsv1alpha1.TestSpec {
	return &testsv1alpha1.TestSpec{Steps: []testsv1alpha1.Step{{
		Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: child}}},
	}}}
}

func mapLookup(m map[string]*testsv1alpha1.TestSpec) TestLookup {
	return func(name string) (*testsv1alpha1.TestSpec, bool) {
		s, ok := m[name]
		return s, ok
	}
}

func nameAt(i int) string {
	return "n" + strings.Repeat("x", i)
}
