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

package scraper

import (
	"path/filepath"

	"github.com/hinskii/kubetest-alt/pkg/executor/k6"
)

// PerfParser is the shared signature: given the wrapper's working directory,
// return the tool-specific metrics as flat name→value pairs.
//
// Step 07 registers only k6 here (the wrapper's k6.Runner already computes
// these; the perf registry is the READ side used by controller-side tooling
// that re-parses summary files without invoking the Runner). Step 11 adds
// jmeter (JTL CSV), locust (CSV), newman (JSON) — same signature.
type PerfParser func(workingDir string) (map[string]float64, error)

// perfRegistry maps executor type ("k6", "cypress", ...) → parser. Read-only
// after init; step 07 wires k6 and step 11 wires the rest at package init.
var perfRegistry = map[string]PerfParser{
	"k6": parseK6,
}

// PerfParserFor returns the parser registered for the given executor type,
// or nil if the type doesn't have one yet (step 11 completes the set).
func PerfParserFor(executorType string) PerfParser {
	return perfRegistry[executorType]
}

// RegisteredPerfTypes returns the executor types with a registered parser.
// Test-friendly enumeration.
func RegisteredPerfTypes() []string {
	out := make([]string, 0, len(perfRegistry))
	for k := range perfRegistry {
		out = append(out, k)
	}
	return out
}

// parseK6 loads summary.json from the working dir and extracts the plan-§11
// standard metrics via pkg/executor/k6.
func parseK6(workingDir string) (map[string]float64, error) {
	s, err := k6.LoadSummary(filepath.Join(workingDir, k6.SummaryFileName))
	if err != nil {
		return nil, err
	}
	return k6.ExtractMetrics(s), nil
}
