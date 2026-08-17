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
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFake_PutGetRoundTrip(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	require.NoError(t, f.Put(ctx, "bucket-a", "run1/hello.txt", strings.NewReader("hello"), 5, "text/plain"))

	rc, err := f.Get(ctx, "bucket-a", "run1/hello.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
}

func TestFake_GetMissingReturnsErrNotFound(t *testing.T) {
	f := NewFake()
	_, err := f.Get(context.Background(), "bucket", "nope")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound), "expected errors.Is(err, ErrNotFound)")
}

func TestFake_PutErrorsQueue(t *testing.T) {
	f := NewFake()
	f.PutErrors = []error{errors.New("first fail"), errors.New("second fail")}

	err1 := f.Put(context.Background(), "b", "k", strings.NewReader("x"), 1, "")
	err2 := f.Put(context.Background(), "b", "k", strings.NewReader("x"), 1, "")
	err3 := f.Put(context.Background(), "b", "k", strings.NewReader("x"), 1, "")

	assert.EqualError(t, err1, "first fail")
	assert.EqualError(t, err2, "second fail")
	assert.NoError(t, err3, "queue drained → success")
	assert.Equal(t, 3, f.PutCalls, "all attempts counted")
}

func TestFake_KeysSorted(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	_ = f.Put(ctx, "b", "z", strings.NewReader("z"), 1, "")
	_ = f.Put(ctx, "b", "a", strings.NewReader("a"), 1, "")
	_ = f.Put(ctx, "b", "m", strings.NewReader("m"), 1, "")
	_ = f.Put(ctx, "other", "a", strings.NewReader("x"), 1, "")

	assert.Equal(t, []string{"a", "m", "z"}, f.Keys("b"))
	assert.Equal(t, []string{"a"}, f.Keys("other"))
	assert.Empty(t, f.Keys("nothing"))
}

func TestFake_Reset(t *testing.T) {
	f := NewFake()
	_ = f.Put(context.Background(), "b", "k", strings.NewReader("x"), 1, "")
	f.PutErrors = []error{errors.New("x")}
	f.Reset()
	assert.Zero(t, f.PutCalls)
	assert.Empty(t, f.Keys("b"))
	assert.Nil(t, f.PutErrors)
}
