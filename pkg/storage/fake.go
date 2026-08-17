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
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
)

// Fake is an in-memory Uploader + Downloader for tests. Concurrent-safe
// so envtest suites with parallel reconciles don't race on it.
//
// Test hooks:
//   - PutError / GetError override the next N calls' return values, so tests
//     assert retry behavior without touching a real network.
//   - Objects exposes the recorded state after a run so assertions can check
//     "bucket X has these exact keys" or read back specific payloads.
type Fake struct {
	mu      sync.Mutex
	objects map[string][]byte // key = "<bucket>/<key>"

	// PutErrors is a queue of errors to return from Put calls, in order.
	// Once drained, Put succeeds normally. Use for retry scenarios.
	PutErrors []error

	// GetErrors mirrors PutErrors for Get.
	GetErrors []error

	// PutCalls / GetCalls count invocations (including failed ones) for
	// tests that need to assert retry counts.
	PutCalls int
	GetCalls int
}

// NewFake builds an empty Fake ready for use.
func NewFake() *Fake {
	return &Fake{objects: map[string][]byte{}}
}

// Put implements Uploader.
func (f *Fake) Put(_ context.Context, bucket, key string, r io.Reader, _ int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PutCalls++
	if len(f.PutErrors) > 0 {
		err := f.PutErrors[0]
		f.PutErrors = f.PutErrors[1:]
		if err != nil {
			return err
		}
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("fake: read: %w", err)
	}
	f.objects[fakeKey(bucket, key)] = b
	return nil
}

// Get implements Downloader.
func (f *Fake) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GetCalls++
	if len(f.GetErrors) > 0 {
		err := f.GetErrors[0]
		f.GetErrors = f.GetErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	b, ok := f.objects[fakeKey(bucket, key)]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// Object reads back the raw bytes previously stored at bucket/key.
// Returns (nil, false) when absent. Test helper only.
func (f *Fake) Object(bucket, key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[fakeKey(bucket, key)]
	if !ok {
		return nil, false
	}
	// Return a copy so callers can't accidentally mutate our state.
	out := make([]byte, len(b))
	copy(out, b)
	return out, true
}

// Keys returns a sorted list of all keys present under the given bucket.
// Empty list if bucket has no objects. Test helper.
func (f *Fake) Keys(bucket string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := bucket + "/"
	var out []string
	for k := range f.objects {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k[len(prefix):])
		}
	}
	slices.Sort(out)
	return out
}

// Reset clears all state (objects + queued errors + counters).
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects = map[string][]byte{}
	f.PutErrors = nil
	f.GetErrors = nil
	f.PutCalls = 0
	f.GetCalls = 0
}

func fakeKey(bucket, key string) string { return bucket + "/" + key }
