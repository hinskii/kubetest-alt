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

package names

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestBounded(t *testing.T) {
	assert.Equal(t, "smoke-1759752000", Bounded("smoke-1759752000"), "short names are untouched")
	exact := strings.Repeat("a", MaxLen)
	assert.Equal(t, exact, Bounded(exact))

	long := strings.Repeat("checkout-flow-", 5) + "1759752000" // 80 chars
	got := Bounded(long)
	assert.Len(t, got, MaxLen)
	assert.Empty(t, validation.IsDNS1123Label(got), "valid as a label value and Job name")
	assert.Equal(t, got, Bounded(long), "deterministic")

	// Different fires of the same long Test differ only past the cut:
	// the hash keeps them apart.
	other := strings.Repeat("checkout-flow-", 5) + "1759755600"
	assert.NotEqual(t, got, Bounded(other))

	// The cut never leaves "-" or "." before the hash suffix.
	dashy := strings.Repeat("a", MaxLen-hashLen-2) + "--" + strings.Repeat("b", 20)
	assert.NotContains(t, Bounded(dashy), "---")
	assert.Empty(t, validation.IsDNS1123Label(Bounded(dashy)))
}

func TestServiceNames(t *testing.T) {
	assert.Equal(t, "nightly-db", ServiceName("nightly", "db"))
	assert.Equal(t, "s-1759752000-db", ServiceName("1759752000", "db"), "must start with a letter")
	assert.Equal(t, "run-v1-2-db", ServiceName("run.v1.2", "db"), "dots are not allowed in Service names")
	long := ServiceName(strings.Repeat("r", 60), "postgres")
	assert.Len(t, long, MaxLen)
	assert.Empty(t, validation.IsDNS1035Label(long))
	assert.Equal(t, "nightly-db-0", ServiceReplicaName("nightly-db", 0))
	assert.Empty(t, validation.IsDNS1123Label(ServiceReplicaName(long, 12)))
	assert.Equal(t, "nightly-db.team-a.svc", ServiceHost("nightly", "team-a", "db"))
}
