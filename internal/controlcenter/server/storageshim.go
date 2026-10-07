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

package server

import (
	"bufio"
	"bytes"
	"io"
)

// storageShim stands in for localStorage and sessionStorage where the
// sandbox denies them. A served HTML artifact runs in an opaque origin
// (artifactCSP), where touching window.localStorage throws a
// SecurityError — and reports that keep their settings there (the
// Playwright HTML report) die on it and show a blank page. The shim keeps
// the values in memory for the page's lifetime; with real storage
// available it does nothing. Methods are non-enumerable so `storage[key]`
// reads and writes, as reports use them, behave like the real thing.
const storageShim = `<script>(function(){function mem(){var s={};var own=function(k){return Object.prototype.hasOwnProperty.call(s,k)};` +
	`Object.defineProperties(s,{getItem:{value:function(k){return own(String(k))?String(s[k]):null}},` +
	`setItem:{value:function(k,v){s[String(k)]=String(v)}},removeItem:{value:function(k){delete s[String(k)]}},` +
	`clear:{value:function(){Object.keys(s).forEach(function(k){delete s[k]})}},` +
	`key:{value:function(i){return Object.keys(s)[i]||null}},length:{get:function(){return Object.keys(s).length}}});return s}` +
	`["localStorage","sessionStorage"].forEach(function(n){try{window[n].length}catch(e){try{Object.defineProperty(window,n,{value:mem(),configurable:true})}catch(e2){}}})})();</script>`

// shimPeek is how far into an HTML artifact the <head> tag is looked for.
const shimPeek = 16 << 10

// copyWithStorageShim copies an HTML document from src to dst with
// storageShim as the first thing in <head> (or, without one near the
// top, right after the doctype — a script before it would switch the page
// to quirks mode).
func copyWithStorageShim(dst io.Writer, src io.Reader) error {
	br := bufio.NewReaderSize(src, shimPeek)
	head, _ := br.Peek(shimPeek) // a shorter document is all there is
	at := insertionPoint(head)
	if _, err := dst.Write(head[:at]); err != nil {
		return err
	}
	if _, err := io.WriteString(dst, storageShim); err != nil {
		return err
	}
	if _, err := br.Discard(at); err != nil {
		return err
	}
	_, err := io.Copy(dst, br)
	return err
}

// insertionPoint is where the shim goes in the document's beginning b:
// after the <head …> tag, else after a leading <!doctype …>, else 0.
func insertionPoint(b []byte) int {
	lower := bytes.ToLower(b)
	for from := 0; ; {
		i := bytes.Index(lower[from:], []byte("<head"))
		if i < 0 {
			break
		}
		i += from
		// <head> or <head attr…>, not <header>.
		if next := i + len("<head"); next < len(lower) && (lower[next] == '>' || isSpace(lower[next])) {
			if end := bytes.IndexByte(lower[next:], '>'); end >= 0 {
				return next + end + 1
			}
			break
		}
		from = i + 1
	}
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(lower, []byte("\xef\xbb\xbf")), " \t\r\n") // BOM, whitespace
	if bytes.HasPrefix(trimmed, []byte("<!doctype")) {
		start := len(lower) - len(trimmed)
		if end := bytes.IndexByte(lower[start:], '>'); end >= 0 {
			return start + end + 1
		}
	}
	return 0
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
