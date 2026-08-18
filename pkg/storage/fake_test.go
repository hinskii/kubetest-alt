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
	"time"

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

func TestFake_RemovePrefix_DropsMatchingKeys(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	// Populate two runs' worth of chunks + a decoy key that must NOT be
	// touched (adjacent prefix, different run).
	_ = f.Put(ctx, "logs", "kubetest-logs/run-A/00000000.log", strings.NewReader("a0"), 2, "")
	_ = f.Put(ctx, "logs", "kubetest-logs/run-A/00000001.log", strings.NewReader("a1"), 2, "")
	_ = f.Put(ctx, "logs", "kubetest-logs/run-A-decoy/x.log", strings.NewReader("dc"), 2, "")
	_ = f.Put(ctx, "logs", "kubetest-logs/run-B/00000000.log", strings.NewReader("b0"), 2, "")

	require.NoError(t, f.RemovePrefix(ctx, "logs", "kubetest-logs/run-A/"))

	assert.Equal(t, []string{
		"kubetest-logs/run-A-decoy/x.log",
		"kubetest-logs/run-B/00000000.log",
	}, f.Keys("logs"))
	assert.Equal(t, 1, f.RemoveCalls)
}

func TestFake_RemovePrefix_EmptyPrefixRejected(t *testing.T) {
	f := NewFake()
	err := f.RemovePrefix(context.Background(), "logs", "")
	require.Error(t, err)
}

func TestFake_RemovePrefix_MissingPrefixIsNoop(t *testing.T) {
	f := NewFake()
	// Empty bucket → no-op, no error.
	require.NoError(t, f.RemovePrefix(context.Background(), "logs", "kubetest-logs/run-X/"))
	assert.Equal(t, 1, f.RemoveCalls)
}

func TestFake_RemovePrefix_ErrorQueue(t *testing.T) {
	f := NewFake()
	f.RemoveErrors = []error{errors.New("boom")}
	err := f.RemovePrefix(context.Background(), "logs", "kubetest-logs/run/")
	require.EqualError(t, err, "boom")
	// Next call succeeds (queue drained).
	require.NoError(t, f.RemovePrefix(context.Background(), "logs", "kubetest-logs/run/"))
}

func TestFake_List_ReturnsSortedKeysUnderPrefix(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	_ = f.Put(ctx, "b", "kubetest-logs/run-A/00000002.log", strings.NewReader("c2"), 2, "")
	_ = f.Put(ctx, "b", "kubetest-logs/run-A/00000000.log", strings.NewReader("c0"), 2, "")
	_ = f.Put(ctx, "b", "kubetest-logs/run-A/00000001.log", strings.NewReader("c1"), 2, "")
	_ = f.Put(ctx, "b", "kubetest-logs/run-B/00000000.log", strings.NewReader("bx"), 2, "")

	keys, err := f.List(ctx, "b", "kubetest-logs/run-A/")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"kubetest-logs/run-A/00000000.log",
		"kubetest-logs/run-A/00000001.log",
		"kubetest-logs/run-A/00000002.log",
	}, keys)
}

func TestFake_List_EmptyPrefixRejected(t *testing.T) {
	f := NewFake()
	_, err := f.List(context.Background(), "b", "")
	require.Error(t, err)
}

func TestFake_PresignGetURL_EncodesExpiry(t *testing.T) {
	f := NewFake()
	u, err := f.PresignGetURL(context.Background(), "artifacts", "run-1/results/junit.xml", 5*time.Minute)
	require.NoError(t, err)
	assert.Contains(t, u, "fake://artifacts/run-1/results/junit.xml")
	assert.Contains(t, u, "expires=300")
	assert.Equal(t, 1, f.PresignCalls)
}

func TestFake_PresignGetURL_ZeroExpiryRejected(t *testing.T) {
	f := NewFake()
	_, err := f.PresignGetURL(context.Background(), "b", "k", 0)
	require.Error(t, err)
}

func TestFake_PresignGetURL_ErrorQueue(t *testing.T) {
	f := NewFake()
	f.PresignErrors = []error{errors.New("no creds")}
	_, err := f.PresignGetURL(context.Background(), "b", "k", time.Minute)
	require.EqualError(t, err, "no creds")
	_, err = f.PresignGetURL(context.Background(), "b", "k", time.Minute)
	require.NoError(t, err)
}
