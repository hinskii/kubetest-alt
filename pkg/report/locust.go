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

// parseLocust — reads the "Aggregated" row of Locust's
// `--csv <prefix>` output, <prefix>_stats.csv (Locust 2.x columns: Type,
// Name, Request Count, Failure Count, Median/Average/Min/Max Response
// Time, ..., Requests/s, ..., 90%, 95%, 99%, ...). Times are milliseconds.

package report

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var columns = map[string]string{
	"Request Count":         Requests,
	"Failure Count":         Errors,
	"Requests/s":            RPS,
	"Average Response Time": LatencyAvgMs,
	"Min Response Time":     LatencyMinMs,
	"Median Response Time":  LatencyMedMs,
	"90%":                   LatencyP90Ms,
	"95%":                   LatencyP95Ms,
	"99%":                   LatencyP99Ms,
	"Max Response Time":     LatencyMaxMs,
}

// Parse maps the Aggregated row; per-endpoint rows are ignored.
func parseLocust(r io.Reader) (map[string]float64, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("locust: read header: %w", err)
	}
	nameCol := -1
	for i, h := range header {
		if strings.TrimSpace(h) == "Name" {
			nameCol = i
		}
	}
	if nameCol < 0 {
		return nil, errors.New("locust: header lacks Name (not a *_stats.csv?)")
	}
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("locust: no Aggregated row")
		}
		if err != nil {
			return nil, fmt.Errorf("locust: read row: %w", err)
		}
		if nameCol >= len(row) || strings.TrimSpace(row[nameCol]) != "Aggregated" {
			continue
		}
		out := map[string]float64{}
		for i, h := range header {
			key, ok := columns[strings.TrimSpace(h)]
			if !ok || i >= len(row) {
				continue
			}
			// Locust writes "N/A" for percentiles with no samples.
			if v, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64); err == nil {
				out[key] = v
			}
		}
		SetErrorRate(out)
		return out, nil
	}
}
