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

// parseJTL — computes load metrics from a JMeter CSV results file
// (`jmeter -n -l results.jtl`, default CSV format with header). JMeter
// reports per-sample rows only, so throughput and percentiles are derived
// here: rps = samples / (last end − first start); percentiles use the
// nearest-rank method over every sample's `elapsed`.
//
// (pkg/verdict/jtl reads the same file for the pass/fail verdict; this
// package never influences the verdict.)

package report

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Parse reads a JTL CSV. Rows too short to carry the required columns
// (JMeter can truncate the last row on crash) are skipped.
func parseJTL(r io.Reader) (map[string]float64, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("jtl: read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	tsCol, ok1 := col["timeStamp"]
	elCol, ok2 := col["elapsed"]
	okCol, ok3 := col["success"]
	if !ok1 || !ok2 || !ok3 {
		return nil, errors.New("jtl: header lacks timeStamp/elapsed/success (not a CSV JTL?)")
	}
	need := max(tsCol, elCol, okCol)

	var (
		elapsed        []float64
		errs           int
		first, lastEnd = math.MaxFloat64, 0.0
	)
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("jtl: read row: %w", err)
		}
		if len(row) <= need {
			continue
		}
		ts, err1 := strconv.ParseFloat(row[tsCol], 64)
		el, err2 := strconv.ParseFloat(row[elCol], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		elapsed = append(elapsed, el)
		if !strings.EqualFold(strings.TrimSpace(row[okCol]), "true") {
			errs++
		}
		first = math.Min(first, ts)
		lastEnd = math.Max(lastEnd, ts+el)
	}
	if len(elapsed) == 0 {
		return nil, errors.New("jtl: no samples")
	}

	slices.Sort(elapsed)
	n := float64(len(elapsed))
	sum := 0.0
	for _, v := range elapsed {
		sum += v
	}
	out := map[string]float64{
		Requests:     n,
		Errors:       float64(errs),
		LatencyAvgMs: sum / n,
		LatencyMinMs: elapsed[0],
		LatencyMedMs: nearestRank(elapsed, 50),
		LatencyP90Ms: nearestRank(elapsed, 90),
		LatencyP95Ms: nearestRank(elapsed, 95),
		LatencyP99Ms: nearestRank(elapsed, 99),
		LatencyMaxMs: elapsed[len(elapsed)-1],
	}
	if spanMs := lastEnd - first; spanMs > 0 {
		out[RPS] = n / (spanMs / 1000)
	}
	SetErrorRate(out)
	return out, nil
}

// nearestRank is the p-th percentile of sorted (non-empty) values.
func nearestRank(sorted []float64, p float64) float64 {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}
