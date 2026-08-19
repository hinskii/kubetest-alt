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

	"github.com/hinskii/kubetest-alt/internal/scraper/perf/k6"
)

// PerfParser is the shared signature: given the wrapper's working directory,
// return the tool-specific metrics as flat name→value pairs.
//
// Workflows model (step 11): the perf registry is indexed by the free-form
// TOOL LABEL (kubetest.io/tool), not by a deleted spec.type. Templates set
// the label; the controller passes it to the scraper; the scraper picks
// which parser to run. New parsers land under internal/scraper/perf/<tool>.
type PerfParser func(workingDir string) (map[string]float64, error)

// perfRegistry maps tool label → parser. Read-only after init.
var perfRegistry = map[string]PerfParser{
	"k6": parseK6,
}

// PerfParserFor returns the parser registered for the given tool label,
// or nil when no parser is registered (metrics simply aren't ingested).
func PerfParserFor(toolLabel string) PerfParser {
	return perfRegistry[toolLabel]
}

// RegisteredPerfTypes returns the tool labels with a registered parser.
// Test-friendly enumeration.
func RegisteredPerfTypes() []string {
	out := make([]string, 0, len(perfRegistry))
	for k := range perfRegistry {
		out = append(out, k)
	}
	return out
}

// parseK6 loads summary.json from the working dir and extracts the
// standard perf metrics via internal/scraper/perf/k6.
func parseK6(workingDir string) (map[string]float64, error) {
	s, err := k6.LoadSummary(filepath.Join(workingDir, k6.SummaryFileName))
	if err != nil {
		return nil, err
	}
	return k6.ExtractMetrics(s), nil
}
