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
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyWithStorageShim_Placement(t *testing.T) {
	cases := map[string]string{
		"<!DOCTYPE html><html><head><title>r</title></head></html>": "<!DOCTYPE html><html><head>" + storageShim + "<title>r</title></head></html>",
		"<!doctype html>\n<HEAD lang=en><meta charset=utf-8>":       "<!doctype html>\n<HEAD lang=en>" + storageShim + "<meta charset=utf-8>",
		"<!DOCTYPE html><header>x</header><p>no head</p>":           "<!DOCTYPE html>" + storageShim + "<header>x</header><p>no head</p>",
		"<p>fragment</p>": storageShim + "<p>fragment</p>",
		"":                storageShim,
	}
	for in, want := range cases {
		var out bytes.Buffer
		require.NoError(t, copyWithStorageShim(&out, strings.NewReader(in)))
		assert.Equal(t, want, out.String(), "input %q", in)
	}
}

func TestCopyWithStorageShim_LargeDocumentIsWhole(t *testing.T) {
	body := "<!DOCTYPE html><html><head></head><body>" + strings.Repeat("x", 3*shimPeek) + "</body></html>"
	var out bytes.Buffer
	require.NoError(t, copyWithStorageShim(&out, strings.NewReader(body)))
	assert.Equal(t, len(body)+len(storageShim), out.Len())
	assert.True(t, strings.HasSuffix(out.String(), "</body></html>"))
}
