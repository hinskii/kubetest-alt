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

// Package jtl parses JMeter Test Log CSV files. Two consumers:
//   - /entry's verdict processor (step 11) — gates pass/fail on the
//     error-rate threshold declared in Test.spec.verdict.errorRateMax.
//   - scraper perf-ingest (step 07+) — extracts metrics into Postgres.
//
// Both consumers depend on the SAME header-driven parse, so the parser
// lives in pkg/ where both can import it. The "verdict" umbrella
// (pkg/verdict/) collects parsers that gate pass/fail; metrics-only
// parsers live under internal/scraper/perf.
package jtl

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Aggregate is the header-driven summary of a JMeter Test Log CSV.
// We only surface the fields the verdict + metrics depend on; the raw
// per-row breakdown stays in the file for post-mortem.
type Aggregate struct {
	// SamplesTotal is the row count (excluding header).
	SamplesTotal int64

	// SamplesFailed is rows with success=false (case-insensitive match).
	SamplesFailed int64

	// ElapsedAvgMs is the arithmetic mean elapsed time (ms) across all
	// samples. Zero when SamplesTotal=0 or the elapsed column is missing.
	ElapsedAvgMs float64
}

// ErrorRate returns SamplesFailed/SamplesTotal, or 0 when SamplesTotal=0
// (nothing failed because nothing ran).
func (a Aggregate) ErrorRate() float64 {
	if a.SamplesTotal == 0 {
		return 0
	}
	return float64(a.SamplesFailed) / float64(a.SamplesTotal)
}

// ErrNotFound is returned by Load when the file is missing. The wrapper
// uses it to distinguish "jmeter crashed before writing" from "the JTL is
// malformed" — the plan requires both surface as phase=error but the
// error messages differ so operators can debug quickly.
var ErrNotFound = errors.New("jtl: file not found")

// ErrNoSuccessColumn is returned when the CSV header lacks the required
// `success` column. Header-driven parsing means we depend on the column
// name, not position — reordered JMeter output still parses cleanly.
var ErrNoSuccessColumn = errors.New("jtl: header missing required `success` column")

// Load opens and parses a JTL file at path.
func Load(path string) (*Aggregate, error) {
	// #nosec G304 -- path is caller-controlled (Runner + working dir).
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse walks the CSV row-by-row. Contract:
//   - Empty file (no header) → error ("empty JTL").
//   - Header present but missing `success` column → ErrNoSuccessColumn.
//     Callers see this as verdict=error with a clean message.
//   - Header present, no rows → SamplesTotal=0, not an error. "Ran the
//     plan, produced nothing" is a legitimate JMeter outcome (empty test
//     group). Downstream verdict logic treats it as "no failures" —
//     matches the locust "0 requests = 0 ratio" contract.
//   - Ragged rows (extra columns): tolerated. csv.Reader.FieldsPerRecord = -1.
func Parse(r io.Reader) (*Aggregate, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("jtl: empty file")
		}
		return nil, fmt.Errorf("jtl: read header: %w", err)
	}
	successCol, elapsedCol := indexColumns(header)
	if successCol < 0 {
		return nil, ErrNoSuccessColumn
	}

	var (
		agg          Aggregate
		elapsedTotal float64
	)
	for {
		row, rerr := reader.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("jtl: read row: %w", rerr)
		}
		if successCol >= len(row) {
			// Skip truncated rows rather than fail — jmeter can emit
			// partial rows on crash and we'd rather see the failure
			// count from the good rows than error out entirely.
			continue
		}
		agg.SamplesTotal++
		if strings.EqualFold(strings.TrimSpace(row[successCol]), "false") {
			agg.SamplesFailed++
		}
		if elapsedCol >= 0 && elapsedCol < len(row) {
			elapsedTotal += atof(row[elapsedCol])
		}
	}
	if agg.SamplesTotal > 0 {
		agg.ElapsedAvgMs = elapsedTotal / float64(agg.SamplesTotal)
	}
	return &agg, nil
}

// indexColumns finds the `success` (required) and `elapsed` (optional)
// columns by case-insensitive name match. Header-driven — column
// position doesn't matter.
func indexColumns(header []string) (successCol, elapsedCol int) {
	successCol, elapsedCol = -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "success":
			successCol = i
		case "elapsed":
			elapsedCol = i
		}
	}
	return successCol, elapsedCol
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}
