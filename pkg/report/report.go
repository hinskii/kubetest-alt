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

// Package report turns a load tool's machine-readable report into the
// flat metric vocabulary every kubetest consumer (run status, history,
// Control Center analytics) uses — see docs/metrics.md.
//
// Which parser runs is declared on the Test (spec.metrics.from), exactly
// like spec.verdict.from: the platform never infers it from the tool label.
// Parsers only read; they never change a run's verdict.
package report

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/bmatcuk/doublestar/v4"
)

// Report formats (spec.metrics.from).
const (
	FromK6Summary     = "k6Summary"     // k6 run --summary-export
	FromJTL           = "jtl"           // JMeter -l results.jtl (CSV)
	FromLocustCSV     = "locustCsv"     // locust --csv <prefix> → <prefix>_stats.csv
	FromGatlingStats  = "gatlingStats"  // <results>/<run>/js/stats.json
	FromArtilleryJSON = "artilleryJson" // artillery run --output report.json
)

// Formats lists every supported format (webhook validation, docs).
var Formats = []string{FromK6Summary, FromJTL, FromLocustCSV, FromGatlingStats, FromArtilleryJSON}

// ErrNoReport means the glob matched nothing — typically the tool crashed
// before writing its report, or the template's output flag and
// spec.metrics.path disagree.
var ErrNoReport = errors.New("report file not found")

var parsers = map[string]func(io.Reader) (map[string]float64, error){
	FromK6Summary:     parseK6,
	FromJTL:           parseJTL,
	FromLocustCSV:     parseLocust,
	FromGatlingStats:  parseGatling,
	FromArtilleryJSON: parseArtillery,
}

// Parse reads one report in the given format.
func Parse(from string, r io.Reader) (map[string]float64, error) {
	p, ok := parsers[from]
	if !ok {
		return nil, fmt.Errorf("unknown report format %q", from)
	}
	return p(r)
}

// ParseFile resolves pathGlob (doublestar, relative to workingDir) and
// parses the match. When several files match, the lexicographically LAST
// one wins: tools that write timestamped result directories (Gatling's
// <simulation>-<epoch>/) put the newest run last.
func ParseFile(from, workingDir, pathGlob string) (map[string]float64, string, error) {
	if !doublestar.ValidatePattern(pathGlob) || filepath.IsAbs(pathGlob) {
		return nil, "", fmt.Errorf("invalid report path %q", pathGlob)
	}
	matches, err := doublestar.Glob(os.DirFS(workingDir), pathGlob, doublestar.WithFilesOnly())
	if err != nil {
		return nil, "", fmt.Errorf("glob %q: %w", pathGlob, err)
	}
	if len(matches) == 0 {
		return nil, "", fmt.Errorf("%w: %s", ErrNoReport, pathGlob)
	}
	slices.Sort(matches)
	rel := matches[len(matches)-1]
	// #nosec G304 -- rel came from a glob rooted at workingDir (os.DirFS
	// rejects escaping paths).
	f, err := os.Open(filepath.Join(workingDir, rel))
	if err != nil {
		return nil, rel, err
	}
	defer func() { _ = f.Close() }()
	m, err := Parse(from, f)
	return m, rel, err
}
