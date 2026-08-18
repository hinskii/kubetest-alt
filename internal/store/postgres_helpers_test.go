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

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParsePartitionBound covers the pg_get_expr → time.Time round-trip.
// Regressions here would break retention (retention.go can't identify
// which partitions to drop if it can't parse the FROM/TO clause).
func TestParsePartitionBound(t *testing.T) {
	cases := []struct {
		name  string
		expr  string
		want  bool
		start string
		end   string
	}{
		{
			name:  "canonical Postgres output",
			expr:  "FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')",
			want:  true,
			start: "2026-07-01T00:00:00Z",
			end:   "2026-08-01T00:00:00Z",
		},
		{
			name:  "with double-digit tz offset",
			expr:  "FOR VALUES FROM ('2026-07-01 02:00:00+02:00') TO ('2026-08-01 02:00:00+02:00')",
			want:  true,
			start: "2026-07-01T00:00:00Z",
			end:   "2026-08-01T00:00:00Z",
		},
		{
			name: "unrelated expression rejected",
			expr: "DEFAULT",
			want: false,
		},
		{
			name: "malformed literal rejected",
			expr: "FOR VALUES FROM ('nope') TO ('nope')",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e, ok := parsePartitionBound(c.expr)
			assert.Equal(t, c.want, ok)
			if !c.want {
				return
			}
			expected, err := time.Parse(time.RFC3339, c.start)
			require.NoError(t, err)
			assert.True(t, s.Equal(expected), "start=%s want=%s", s, c.start)
			expected2, err := time.Parse(time.RFC3339, c.end)
			require.NoError(t, err)
			assert.True(t, e.Equal(expected2), "end=%s want=%s", e, c.end)
		})
	}
}

// TestJSONBOrNil ensures empty containers land as NULL (jsonb column stays
// null-able) rather than "{}" or "[]" which would break IS NULL queries.
func TestJSONBOrNil(t *testing.T) {
	assert.Nil(t, jsonbOrNil(map[string]any(nil)))
	assert.Nil(t, jsonbOrNil(map[string]any{}))
	assert.Nil(t, jsonbOrNil(map[string]string{}))
	assert.Nil(t, jsonbOrNil(map[string]float64{}))
	assert.Nil(t, jsonbOrNil([]ArtifactRef{}))
	assert.Nil(t, jsonbOrNil((*TestCounts)(nil)))

	// Non-empty → bytes.
	b := jsonbOrNil(map[string]float64{"x": 1})
	assert.NotNil(t, b)
}

// TestNullIf helpers pin the empty-string / zero-int → NULL behavior.
func TestNullIfHelpers(t *testing.T) {
	assert.Nil(t, nullIfEmpty(""))
	assert.Equal(t, "hi", nullIfEmpty("hi"))
	assert.Nil(t, nullIfZero(0))
	assert.Equal(t, int64(42), nullIfZero(42))
}

// TestPostgres_SaveFinished_NotTerminalShortCircuit — non-terminal phases
// must return ErrNotTerminal WITHOUT touching the pool, so this test uses
// a nil-pool Postgres and asserts no panic. The guard belongs on the write
// path because a webhook or a mid-flight edit could theoretically hand the
// store a live run.
func TestPostgres_SaveFinished_NotTerminalShortCircuit(t *testing.T) {
	p := NewPostgres(nil) // no pool — any DB access would panic
	run := mkTerminalRun(t)
	run.Status.Phase = "running"
	err := p.SaveFinished(t.Context(), run)
	require.ErrorIs(t, err, ErrNotTerminal)
}

// TestPostgres_SaveFinished_NilRunShortCircuit — same guard, nil run.
func TestPostgres_SaveFinished_NilRunShortCircuit(t *testing.T) {
	p := NewPostgres(nil)
	err := p.SaveFinished(t.Context(), nil)
	require.ErrorIs(t, err, ErrNotTerminal)
}
