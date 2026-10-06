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

// Package names builds the names the platform generates itself: TestRuns
// (cron fires, composite children) and spec.services objects. A TestRun
// name becomes the Job name and label values, so it must fit a label
// value: 63 characters.
package names

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// ServiceName is the headless Service of service svc of run runName: a
// DNS-1035 label (Service names must start with a letter; run names may
// start with a digit or contain dots).
func ServiceName(runName, svc string) string {
	n := []byte(strings.ToLower(runName + "-" + svc))
	for i, c := range n {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			n[i] = '-'
		}
	}
	name := string(n)
	if name[0] < 'a' || name[0] > 'z' {
		name = "s-" + name
	}
	return Bounded(name)
}

// ServiceReplicaName is replica i of a service: its Pod name and hostname.
func ServiceReplicaName(serviceName string, i int) string {
	return Bounded(fmt.Sprintf("%s-%d", serviceName, i))
}

// ServiceHost is the in-cluster DNS name of a run's service (every ready
// replica).
func ServiceHost(runName, namespace, svc string) string {
	return ServiceName(runName, svc) + "." + namespace + ".svc"
}
