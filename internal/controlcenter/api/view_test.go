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

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

func TestNextFires(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 30, 0, 0, time.UTC)
	got, err := NextFires("0 2 * * *", now, 3)
	require.NoError(t, err)
	assert.Equal(t, []time.Time{
		time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC),
	}, got, "UTC without a zone")

	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	got, err = NextFires("0 2 * * *", now.In(warsaw), 1)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC), got[0], "UTC whatever zone Control Center runs in")

	got, err = NextFires("CRON_TZ=Europe/Warsaw 0 2 * * *", now, 1)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), got[0], "02:00 in Warsaw (CEST) is 00:00 UTC")

	_, err = NextFires("every night", now, 1)
	require.Error(t, err)
	_, err = NextFires("0 0 0 2 * *", now, 1)
	require.Error(t, err, "six fields (seconds) are not the operator's format")
}

func TestScheduleOf(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 30, 0, 0, time.UTC)
	assert.Nil(t, ScheduleOf("", now))
	s := ScheduleOf("61 * * * *", now)
	require.NotNil(t, s)
	assert.NotEmpty(t, s.Error, "never fires")
	assert.Equal(t, []time.Time{}, s.Next)
}

func TestGeneratePrefix(t *testing.T) {
	assert.Equal(t, "smoke-", GeneratePrefix("smoke"))
	assert.Len(t, GeneratePrefix(strings.Repeat("a", 70)), 51)
	assert.Equal(t, "a-", GeneratePrefix("a"+strings.Repeat("-", 49)+"b"), "no trailing dashes before ours")
}

func TestRunID(t *testing.T) {
	assert.Equal(t, "smoke-1", RunID(apiclient.Run{Name: "smoke-1", UID: "u"}))
	assert.Equal(t, "u", RunID(apiclient.Run{Name: "smoke-1", UID: "u", Origin: "archive"}))
}

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
		assert.Equal(t, want, CommitURL(repo, sha), repo)
	}
	assert.Empty(t, CommitURL("https://github.com/acme/tests", ""), "no commit, no link")
}
