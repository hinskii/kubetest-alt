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

package resolver

import (
	"regexp"
	"slices"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// RepoDir is where a Test's files are, in the pod: the git checkout, or
// inline files under repo/ (CLAUDE.md §12). Templates point their tool at
// a path below it.
const RepoDir = "/data/repo"

// mainPathRef is a parameter placed right below RepoDir.
var mainPathRef = regexp.MustCompile(regexp.QuoteMeta(RepoDir) + `/\{\{\s*config\.([A-Za-z0-9_]+)\s*\}\}`)

// MainPathParam is the parameter naming where the tool finds the Test's
// files — the one c puts right after /data/repo/ in its command,
// arguments or working directory: k6's script, JMeter's plan, newman's
// collection, playwright's projectDir, … — or "" when there is none (a
// tool aimed at a URL, an own image). It is read from the container
// itself, so no tool is named in code. A Test must give it a value: a
// template never defaults it (step 20h).
func MainPathParam(c testsv1alpha1.ContainerConfig, config map[string]testsv1alpha1.Parameter) string {
	for _, s := range slices.Concat(c.Command, c.Args, []string{c.WorkingDir}) {
		for _, m := range mainPathRef.FindAllStringSubmatch(s, -1) {
			if _, ok := config[m[1]]; ok {
				return m[1]
			}
		}
	}
	return ""
}
