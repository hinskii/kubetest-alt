/*
Copyright 2026.
*/

package composer

import (
	"regexp"
	"strconv"
)

// index/count patterns. Tolerant of internal whitespace only (matches
// pkg/expr's own leniency). Non-greedy scope names — extras like
// {{ index.foo }} intentionally do NOT match so a typo surfaces the
// leftover literal at child-side resolve.
var (
	reIndex = regexp.MustCompile(`\{\{\s*index\s*\}\}`)
	reCount = regexp.MustCompile(`\{\{\s*count\s*\}\}`)
)

// RenderChildConfig renders a StepExecuteTest.Config map for one
// specific replica by substituting `{{ index }}` and `{{ count }}`.
// Every OTHER `{{ ... }}` reference (config.*, env.*, run.id, …) is
// left AS-IS so the child TestRun's own resolveSpec pipeline (step
// 13) picks them up at child setup time. Non-templated values pass
// through unchanged (no regex work).
//
// Kept regex-based (not routed through pkg/expr) because the scope
// here is a fixed 2-item namespace — reusing the strict expression
// engine would require inventing an "unresolved-ident is fine" mode
// that doesn't fit its contract.
//
// index is 0-based replica index within a StepExecuteTest.Count.
// count is the total replica count for that reference.
func RenderChildConfig(raw map[string]string, index, count int32) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	idxStr := strconv.Itoa(int(index))
	cntStr := strconv.Itoa(int(count))
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		v = reIndex.ReplaceAllString(v, idxStr)
		v = reCount.ReplaceAllString(v, cntStr)
		out[k] = v
	}
	return out
}
