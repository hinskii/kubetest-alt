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

package storage

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunKeys_Layout(t *testing.T) {
	k := ForRun("team-a", "0b6f5a4e-1111-2222-3333-444455556666")
	assert.True(t, k.Valid())
	assert.Equal(t, "runs/team-a/0b6f5a4e-1111-2222-3333-444455556666/", k.Prefix())
	assert.Equal(t, k.Prefix()+"result.json", k.Result())
	assert.Equal(t, k.Prefix()+"logs/", k.Logs())
	assert.Equal(t, k.Prefix()+"logs/00000007.log", k.LogChunk(7))
	assert.Equal(t, k.Prefix()+"artifacts/reports/junit.xml", k.Artifact("reports/junit.xml"))
}

func TestRunKeys_EmptyPartsAreInvalid(t *testing.T) {
	assert.False(t, ForRun("", "uid").Valid())
	assert.False(t, ForRun("ns", "").Valid())
}

func TestParseRunKeys(t *testing.T) {
	good := ForRun("ns", "uid")
	got, err := ParseRunKeys(good.Prefix())
	assert.NoError(t, err)
	assert.Equal(t, good, got)

	for _, bad := range []string{
		"", "runs/", "runs/ns/", "runs/ns/uid", "runs/ns/uid/extra/",
		"other/ns/uid/", "runs//uid/", "runs/../uid/", "runs/ns/../",
	} {
		_, err := ParseRunKeys(bad)
		assert.Error(t, err, "prefix %q should be rejected", bad)
	}
}

// Two runs with the same name but different namespaces/UIDs must not share
// any key — this was the cross-namespace overwrite bug.
func TestRunKeys_NoCrossRunCollision(t *testing.T) {
	a := ForRun("ns-a", "uid-1")
	b := ForRun("ns-b", "uid-1")
	c := ForRun("ns-a", "uid-2")
	for _, other := range []RunKeys{b, c} {
		assert.False(t, strings.HasPrefix(other.Prefix(), a.Prefix()))
		assert.False(t, strings.HasPrefix(a.Prefix(), other.Prefix()))
	}
}

// A user artifact named result.json must not land on the verdict key.
func TestRunKeys_ArtifactCannotShadowResult(t *testing.T) {
	k := ForRun("ns", "uid")
	assert.NotEqual(t, k.Result(), k.Artifact("result.json"))
}

// Lexicographic order of chunk keys must equal numeric order (the API
// server streams chunks in List order).
func TestRunKeys_ChunkKeysSortNumerically(t *testing.T) {
	k := ForRun("ns", "uid")
	assert.Less(t, k.LogChunk(9), k.LogChunk(10))
	assert.Less(t, k.LogChunk(99999), k.LogChunk(100000))
}
