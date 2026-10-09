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

// Package yamlview writes Kubernetes objects as YAML for people (step
// 20j): keys in a reading order instead of alphabetically, multi-line
// strings as `|` blocks, no empty objects — and highlights it for HTML.
package yamlview

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Root says what the value is, which decides the key order at the top.
type Root int

const (
	// Manifest: apiVersion, kind, metadata, spec, … (a whole object).
	Manifest Root = iota
	// Spec: a TestSpec on its own (a Test page's definition).
	Spec
)

// specKey is the TestSpec's key, and the path of a spec rendered alone.
const specKey = "spec"

// keyOrder lists the keys that come first in a mapping, by the mapping's
// path ("" the root, "metadata", "spec", "spec.config.*", …); the rest
// keep their order (the Go struct's, from the JSON encoding).
var keyOrder = map[string][]string{
	"":                     {"apiVersion", "kind", "metadata", "spec", "status"},
	"metadata":             {"name", "namespace", "labels", "annotations"},
	"spec":                 {"use", "steps", "config", "content", "container", "pod", "timeout", "schedule"},
	"spec.config.*":        {"type", "default", "path", "description", "enum", "pattern"},
	"spec.content":         {"git", "files", "tarball", "testData"},
	"spec.container":       {"image", "command", "args", "workingDir", "env", "envFrom", "resources"},
	"spec.content.git":     {"uri", "revision", "paths"},
	"spec.content.files[]": {"path", "content"},
}

// Format renders v (anything encoding/json takes) as YAML for people.
func Format(v any, root Root) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil { // JSON is YAML
		return "", err
	}
	if len(doc.Content) == 0 {
		return "", nil
	}
	top := doc.Content[0]
	path := ""
	if root == Spec {
		path = specKey
	}
	tidy(top, path, path == specKey)
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(top); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// tidy orders keys, drops empty mappings (the Go types leave
// "resources: {}"), and styles scalars: multi-line strings as blocks,
// plain ones unquoted where YAML allows. keepEmpty keeps n itself even
// if empty ("spec: {}").
func tidy(n *yaml.Node, path string, keepEmpty bool) (empty bool) {
	switch n.Kind {
	case yaml.MappingNode:
		pairs := make([][2]*yaml.Node, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := joinPath(path, k.Value)
			if tidy(v, child, false) {
				continue
			}
			k.Style = 0
			pairs = append(pairs, [2]*yaml.Node{k, v})
		}
		order := keyOrder[path]
		if order == nil {
			order = keyOrder[wildcard(path)]
		}
		rank := func(key string) int {
			if i := slices.Index(order, key); i >= 0 {
				return i
			}
			return len(order)
		}
		slices.SortStableFunc(pairs, func(a, b [2]*yaml.Node) int { return rank(a[0].Value) - rank(b[0].Value) })
		n.Content = n.Content[:0]
		for _, p := range pairs {
			n.Content = append(n.Content, p[0], p[1])
		}
		n.Style = 0
		return len(pairs) == 0 && !keepEmpty
	case yaml.SequenceNode:
		// Items keep their content even when empty: in a list an empty
		// object can mean something (a volume's "emptyDir: {}").
		for _, item := range n.Content {
			if item.Kind == yaml.MappingNode {
				tidyItem(item, path+"[]")
			} else {
				tidy(item, path+"[]", true)
			}
		}
		n.Style = 0
		return false
	case yaml.ScalarNode:
		n.Style = 0
		if n.Tag == "!!str" && strings.Contains(strings.TrimRight(n.Value, "\n"), "\n") && blockable(n.Value) {
			n.Style = yaml.LiteralStyle
		}
		return false
	}
	return false
}

// tidyItem tidies a list item's fields but keeps the item and its empty
// objects.
func tidyItem(n *yaml.Node, path string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		n.Content[i].Style = 0
		v := n.Content[i+1]
		if v.Kind == yaml.MappingNode && len(v.Content) == 0 {
			v.Style = yaml.FlowStyle // "emptyDir: {}"
			continue
		}
		tidy(v, joinPath(path, n.Content[i].Value), true)
	}
	order := keyOrder[path]
	if order != nil {
		pairs := make([][2]*yaml.Node, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			pairs = append(pairs, [2]*yaml.Node{n.Content[i], n.Content[i+1]})
		}
		rank := func(key string) int {
			if i := slices.Index(order, key); i >= 0 {
				return i
			}
			return len(order)
		}
		slices.SortStableFunc(pairs, func(a, b [2]*yaml.Node) int { return rank(a[0].Value) - rank(b[0].Value) })
		n.Content = n.Content[:0]
		for _, p := range pairs {
			n.Content = append(n.Content, p[0], p[1])
		}
	}
	n.Style = 0
}

// blockable: a `|` block keeps the string exactly — not with trailing
// spaces on a line, tabs, or control characters.
func blockable(s string) bool {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasSuffix(line, " ") || strings.ContainsAny(line, "\t\r") {
			return false
		}
	}
	return true
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// wildcard turns "spec.config.vus" into "spec.config.*".
func wildcard(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[:i] + ".*"
	}
	return path
}
