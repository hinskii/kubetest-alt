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

// Package names builds the TestRun names the platform generates itself
// (cron fires, composite children). A TestRun name becomes the Job name
// and label values, so it must fit a label value: 63 characters.
package names

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// MaxLen is the longest TestRun name: Job names and the label values that
// carry run names (kubetest.io/run-id, batch job-name) are limited to 63.
const MaxLen = 63

// hashLen is the length of the uniqueness suffix of a shortened name.
const hashLen = 8

// Bounded returns name when it fits MaxLen. A longer name is cut and
// suffixed with a hash of the full name: deterministic (the same input
// always yields the same name, which keeps cron fires and child creation
// idempotent) and still unique in practice.
func Bounded(name string) string {
	if len(name) <= MaxLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	prefix := strings.TrimRight(name[:MaxLen-hashLen-1], "-.")
	return prefix + "-" + hex.EncodeToString(sum[:])[:hashLen]
}
