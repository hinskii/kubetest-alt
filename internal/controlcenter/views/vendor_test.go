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
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVendoredUI_MatchesThePinnedVersions: the libraries under
// static/vendor are the versions the Makefile pins (make vendor-ui), with
// their licenses — nobody edits a vendored file by hand.
func TestVendoredUI_MatchesThePinnedVersions(t *testing.T) {
	mk, err := os.ReadFile("../../../Makefile")
	require.NoError(t, err)
	pin := regexp.MustCompile(`(?m)^TOM_SELECT_VERSION \?= (\S+)$`).FindSubmatch(mk)
	require.NotNil(t, pin, "TOM_SELECT_VERSION in the Makefile")
	got, err := staticFS.ReadFile("static/vendor/tom-select/VERSION")
	require.NoError(t, err)
	assert.Equal(t, "tom-select "+string(pin[1]), strings.TrimSpace(string(got)), "run make vendor-ui")
	for _, f := range []string{"LICENSE", "tom-select.complete.min.js", "tom-select.css"} {
		_, err := staticFS.ReadFile("static/vendor/tom-select/" + f)
		assert.NoError(t, err, f)
	}
}
