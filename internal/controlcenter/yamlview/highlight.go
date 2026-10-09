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

package yamlview

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Highlighting is Chroma's (the highlighter Hugo and Gitea use): CSS
// classes, line numbers, the github / github-dark styles.
var formatter = chromahtml.New(chromahtml.WithClasses(true), chromahtml.WithLineNumbers(true), chromahtml.TabWidth(2))

// Highlight renders YAML as highlighted HTML.
func Highlight(text string) template.HTML {
	return HighlightAs(text, "yaml")
}

// HighlightAs renders source in a language Chroma knows by name or file
// name (yaml, load.js, locustfile.py, plan.jmx, …); unknown ones plain.
func HighlightAs(text, lang string) template.HTML {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Match(lang)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	var b bytes.Buffer
	if err == nil {
		err = formatter.Format(&b, styles.Get("github"), it)
	}
	if err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(text) + "</pre>") // #nosec G203 -- escaped
	}
	return template.HTML(b.String()) // #nosec G203 -- Chroma escapes the source
}

// Each style is scoped to its Control Center theme, so the light one's
// colours never leak into the dark (a token the dark style doesn't colour
// would keep the light colour — dark on dark).
const (
	lightSelector = `:root:not([data-theme="dark"]) `
	darkSelector  = `:root[data-theme="dark"] `
)

// CSS is the stylesheet for highlighted code: github in the light theme,
// github-dark in the dark one; the background is left to the page.
func CSS() string {
	var light, dark bytes.Buffer
	_ = formatter.WriteCSS(&light, styles.Get("github"))
	_ = formatter.WriteCSS(&dark, styles.Get("github-dark"))
	scope := func(css, sel string) string {
		css = strings.ReplaceAll(css, ".chroma", sel+".chroma")
		return strings.ReplaceAll(css, "*/ .bg ", "*/ "+sel+".bg ")
	}
	return scope(light.String(), lightSelector) + scope(dark.String(), darkSelector) +
		".chroma, .bg { background-color: transparent !important; }\n"
}
