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

package views

import (
	"fmt"
	"html/template"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
)

// funcs are available in every template. There is deliberately no query
// escaper: html/template encodes values after "?" in URL attributes itself,
// so escaping them again breaks the link.
var funcs = template.FuncMap{
	"when":         when,
	"duration":     duration,
	"phaseClass":   func(p any) string { return "status-" + fmt.Sprint(p) },
	"terminal":     terminal,
	"sortedKeys":   sortedKeys,
	"toYAML":       toYAML,
	"pathEscape":   url.PathEscape,
	"canRun":       func(u auth.User) bool { return u.Can(auth.RoleDeveloper) },
	"canDelete":    func(u auth.User) bool { return u.Can(auth.RoleAdmin) },
	"hasPrefix":    strings.HasPrefix,
	"artifactPath": artifactPath,
	"pct":          pct,
	"deltaMs":      deltaMs,
	"sub":          func(a, b int) int { return a - b },
	"dict":         dict,
	"shortSHA":     shortSHA,
	"commitURL":    commitURL,
}

// shortSHA is the 7-character form of a commit SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// commitURL links a commit on GitHub, GitLab or Bitbucket (cloud or a
// self-hosted instance with the name in its host), from an https, ssh://
// or scp-style (git@host:owner/repo.git) repository URI. "" for anything
// else — the page then shows the SHA without a link.
func commitURL(repo, sha string) string {
	if sha == "" {
		return ""
	}
	var host, path string
	if u, err := url.Parse(repo); err == nil && u.Host != "" {
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else if at, colon := strings.Index(repo, "@"), strings.Index(repo, ":"); at >= 0 && colon > at {
		host, path = repo[at+1:colon], repo[colon+1:]
	} else {
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || strings.Count(path, "/") < 1 {
		return ""
	}
	base := "https://" + host + "/" + path
	switch h := strings.ToLower(host); {
	case strings.Contains(h, "github"):
		return base + "/commit/" + url.PathEscape(sha)
	case strings.Contains(h, "gitlab"):
		return base + "/-/commit/" + url.PathEscape(sha)
	case strings.Contains(h, "bitbucket"):
		return base + "/commits/" + url.PathEscape(sha)
	}
	return ""
}

// dict builds a map for passing several values to a sub-template:
// {{template "x" (dict "Title" "…" "Rows" .Rows)}}.
func dict(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return m
}

// pct renders part/total as a whole percentage, "" when total is 0.
func pct(part, total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", (part*100+total/2)/total)
}

// deltaMs renders a duration change: "+1.2s", "-300ms", "" for none.
func deltaMs(d int64) string {
	if d == 0 {
		return ""
	}
	sign := "+"
	if d < 0 {
		sign, d = "-", -d
	}
	if d < 1000 {
		return fmt.Sprintf("%s%dms", sign, d)
	}
	return sign + duration(d)
}

// when renders a time (or *time.Time) in UTC, "" for none.
func when(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return ""
		}
		t = *x
	default:
		return ""
	}
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

// duration renders milliseconds as "1h 2m", "3m 4s", "5.6s", "" for 0.
func duration(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

// terminal reports a finished run phase.
func terminal(p any) bool {
	switch fmt.Sprint(p) {
	case "passed", "failed", "error", "aborted":
		return true
	}
	return false
}

// sortedKeys returns a string-keyed map's keys in order (templates range
// maps in key order already; this is for "is it empty" + ordered loops
// over computed maps).
func sortedKeys(m any) []string {
	v := reflect.ValueOf(m)
	if v.Kind() != reflect.Map {
		return nil
	}
	keys := make([]string, 0, v.Len())
	for _, k := range v.MapKeys() {
		keys = append(keys, k.String())
	}
	slices.SortFunc(keys, NaturalCompare)
	return keys
}

// NaturalCompare orders strings with numbers by value: "worker-2" before
// "worker-10".
func NaturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitsPrefix(a), digitsPrefix(b)
		if da != "" && db != "" {
			if len(da) != len(db) {
				return len(da) - len(db)
			}
			if c := strings.Compare(da, db); c != 0 {
				return c
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digitsPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// toYAML renders a value as YAML for read-only display.
func toYAML(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// artifactPath escapes each segment of an artifact path for a URL.
func artifactPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
