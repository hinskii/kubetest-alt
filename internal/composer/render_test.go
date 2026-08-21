/*
Copyright 2026.
*/

package composer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderChildConfig_Table(t *testing.T) {
	cases := []struct {
		name  string
		raw   map[string]string
		index int32
		count int32
		want  map[string]string
	}{
		{"nil in → nil out", nil, 0, 3, nil},
		{"empty in → nil out", map[string]string{}, 0, 3, nil},
		{"index token", map[string]string{"shard": "{{ index }}"}, 2, 5, map[string]string{"shard": "2"}},
		{"count token", map[string]string{"total": "{{ count }}"}, 0, 5, map[string]string{"total": "5"}},
		{"both tokens in one value", map[string]string{"tag": "worker-{{ index }}-of-{{ count }}"}, 3, 8, map[string]string{"tag": "worker-3-of-8"}},
		{"unknown scope pass-through (child resolves)", map[string]string{"user": "{{ config.foo }}"}, 0, 1, map[string]string{"user": "{{ config.foo }}"}},
		{"typo like {{ index.foo }} left as-is (child errors on it)", map[string]string{"x": "{{ index.foo }}"}, 0, 1, map[string]string{"x": "{{ index.foo }}"}},
		{"plain string untouched", map[string]string{"k": "v"}, 0, 1, map[string]string{"k": "v"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderChildConfig(tc.raw, tc.index, tc.count)
			assert.Equal(t, tc.want, got)
		})
	}
}
