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

// Package server is Control Center's HTTP layer. It is stateless: every
// page is built from the clusters' kubetest API servers on request.
package server

import (
	"net/url"
	"strings"

	uiapi "github.com/hinskii/kubetest-alt/internal/controlcenter/api"
)

// The server-rendered editor's HTML form (until the editor moves to the
// UI, step 20R R4): its field names and how a submitted form becomes the
// editor's model.

// Form field names.
const (
	fieldNamespace = "namespace"
	fieldName      = "name"
	fieldTemplate  = "template"
	fieldFilePath  = "file.path"
	fieldFileBody  = "file.content"
	// fieldParam prefixes a template parameter: param.<template>.<name>.
	fieldParam = "param."
)

// otherNamespace is the namespace list's "Other namespace…" choice: the
// name is then typed.
const otherNamespace = "__other"

// namespaceOf is the chosen namespace: from the list, else typed.
func namespaceOf(v url.Values) string {
	if c := v.Get("namespaceChoice"); c != "" && c != otherNamespace {
		return c
	}
	return strings.TrimSpace(v.Get(fieldNamespace))
}

// formFromValues reads a submitted form.
func formFromValues(v url.Values) uiapi.TestForm {
	f := uiapi.TestForm{
		Namespace: namespaceOf(v), Name: strings.TrimSpace(v.Get(fieldName)),
		Template: v.Get(fieldTemplate), Tool: strings.TrimSpace(v.Get("tool")),
		Image: strings.TrimSpace(v.Get("image")), Command: v.Get("command"), Args: v.Get("args"),
		Source: v.Get("source"), GitURI: strings.TrimSpace(v.Get("gitURI")),
		GitRevision: strings.TrimSpace(v.Get("gitRevision")), GitPaths: v.Get("gitPaths"),
		GitPath:   strings.Trim(strings.TrimSpace(v.Get("gitPath")), "/"),
		GitSecret: strings.TrimSpace(v.Get("gitSecret")), GitSecretKey: strings.TrimSpace(v.Get("gitSecretKey")),
		Env:        v.Get("env"),
		CPURequest: strings.TrimSpace(v.Get("cpuRequest")), MemoryRequest: strings.TrimSpace(v.Get("memoryRequest")),
		CPULimit: strings.TrimSpace(v.Get("cpuLimit")), MemoryLimit: strings.TrimSpace(v.Get("memoryLimit")),
		ServiceAccount: strings.TrimSpace(v.Get("serviceAccount")),
		PodAnnotations: v.Get("podAnnotations"), PodLabels: v.Get("podLabels"),
		Timeout: strings.TrimSpace(v.Get("timeout")), Schedule: strings.Join(strings.Fields(v.Get("schedule")), " "),
		Mode: v.Get("mode"), YAML: v.Get("yaml"), ResourceVersion: v.Get("resourceVersion"),
	}
	kinds, names, wheres, mounts, items := v["td.kind"], v["td.name"], v["td.where"], v["td.mountPath"], v["td.items"]
	for i, name := range names {
		at := func(xs []string) string {
			if i < len(xs) {
				return strings.TrimSpace(xs[i])
			}
			return ""
		}
		if name = strings.TrimSpace(name); name != "" {
			f.TestData = append(f.TestData, uiapi.TestDataField{Kind: at(kinds), Name: name, Where: at(wheres),
				MountPath: at(mounts), Items: at(items)})
		}
	}
	paths, bodies := v[fieldFilePath], v[fieldFileBody]
	for i, p := range paths {
		body := ""
		if i < len(bodies) {
			body = strings.ReplaceAll(bodies[i], "\r\n", "\n")
		}
		if p = strings.TrimSpace(p); p != "" || strings.TrimSpace(body) != "" {
			f.Files = append(f.Files, uiapi.FileField{Path: p, Content: body})
		}
	}
	prefix := fieldParam + f.Template + "."
	f.Params = map[string]string{}
	for k, vals := range v {
		if name, ok := strings.CutPrefix(k, prefix); ok && f.Template != "" && len(vals) > 0 {
			f.Params[name] = strings.TrimSpace(vals[0])
		}
	}
	f.Normalize()
	return f
}
