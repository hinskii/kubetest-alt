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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCommitURL(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for repo, want := range map[string]string{
		"https://github.com/acme/tests.git":            "https://github.com/acme/tests/commit/" + sha,
		"https://github.com/acme/tests/":               "https://github.com/acme/tests/commit/" + sha,
		"git@github.com:acme/tests.git":                "https://github.com/acme/tests/commit/" + sha,
		"ssh://git@gitlab.example.com/group/sub/tests": "https://gitlab.example.com/group/sub/tests/-/commit/" + sha,
		"https://bitbucket.org/acme/tests.git":         "https://bitbucket.org/acme/tests/commits/" + sha,
		"https://git.example.com/acme/tests.git":       "", // unknown host
		"https://github.com/":                          "", // no repository path
		"file:///srv/git/tests.git":                    "",
		"javascript:alert(1)":                          "",
		"":                                             "",
	} {
		assert.Equal(t, want, commitURL(repo, sha), repo)
	}
	assert.Empty(t, commitURL("https://github.com/acme/tests", ""), "no commit, no link")
}

func TestShortSHA(t *testing.T) {
	assert.Equal(t, "0123456", shortSHA("0123456789abcdef"))
	assert.Equal(t, "abc", shortSHA("abc"))
}
