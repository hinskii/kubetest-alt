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

// parseZAP — reads ZAP's traditional JSON report (zap-baseline.py -J,
// also the full and API scans). Counts alert TYPES by risk, summed over
// the scanned sites: what ZAP's own summary counts ("WARN-NEW: 7"), not
// how many URLs each alert was seen on.

package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type zapReport struct {
	Site []struct {
		Alerts []struct {
			RiskCode string `json:"riskcode"`
		} `json:"alerts"`
	} `json:"site"`
}

func parseZAP(r io.Reader) (map[string]float64, error) {
	var rep zapReport
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return nil, fmt.Errorf("zap report: %w", err)
	}
	if rep.Site == nil {
		return nil, errors.New("zap report: no site block")
	}
	byRisk := map[string]string{"3": AlertsHigh, "2": AlertsMedium, "1": AlertsLow, "0": AlertsInfo}
	out := map[string]float64{AlertsHigh: 0, AlertsMedium: 0, AlertsLow: 0, AlertsInfo: 0, AlertsTotal: 0}
	for _, site := range rep.Site {
		for _, a := range site.Alerts {
			key, ok := byRisk[a.RiskCode]
			if !ok {
				return nil, fmt.Errorf("zap report: unknown riskcode %q", a.RiskCode)
			}
			out[key]++
			out[AlertsTotal]++
		}
	}
	return out, nil
}
