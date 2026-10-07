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

// parseKubepug — reads `kubepug --format json`: API versions the scanned
// manifests use that are deprecated, or already deleted, in the target
// Kubernetes version. Counts the API versions found in each list and the
// objects that use any of them. kubepug lists an API's objects under
// "deleted_items" in both lists; "deprecated_items" is read too.

package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type kubepugAPI struct {
	Kind            string            `json:"kind"`
	DeletedItems    []json.RawMessage `json:"deleted_items"`
	DeprecatedItems []json.RawMessage `json:"deprecated_items"`
}

func parseKubepug(r io.Reader) (map[string]float64, error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("kubepug report: %w", err)
	}
	_, hasDeprecated := raw["deprecated_apis"]
	_, hasDeleted := raw["deleted_apis"]
	if !hasDeprecated || !hasDeleted {
		return nil, errors.New("kubepug report: no deprecated_apis/deleted_apis")
	}
	var deprecated, deleted []kubepugAPI
	if err := unmarshalNullable(raw["deprecated_apis"], &deprecated); err != nil {
		return nil, fmt.Errorf("kubepug report: deprecated_apis: %w", err)
	}
	if err := unmarshalNullable(raw["deleted_apis"], &deleted); err != nil {
		return nil, fmt.Errorf("kubepug report: deleted_apis: %w", err)
	}
	objects := 0
	for _, list := range [][]kubepugAPI{deprecated, deleted} {
		for _, api := range list {
			objects += len(api.DeletedItems) + len(api.DeprecatedItems)
		}
	}
	return map[string]float64{
		DeprecatedAPIs:  float64(len(deprecated)),
		DeletedAPIs:     float64(len(deleted)),
		AffectedObjects: float64(objects),
	}, nil
}

func unmarshalNullable(b json.RawMessage, v any) error {
	if string(b) == "null" {
		return nil
	}
	return json.Unmarshal(b, v)
}
