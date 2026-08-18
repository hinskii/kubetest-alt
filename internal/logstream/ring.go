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

package logstream

import "sync"

// ringBuffer is a bounded byte buffer with FIFO overflow. It exists so a
// late-joining subscriber can replay the last N bytes of the log before
// receiving live tail — the piece §12 calls "small ring-buffer in the API
// server for reconnects".
//
// Concurrent-safe: all methods take a lock; callers can Append from the tail
// goroutine while a controller-side handler calls Snapshot.
type ringBuffer struct {
	mu       sync.Mutex
	capacity int
	buf      []byte // len(buf) == capacity when full; grows to capacity then overwrites
	// tail is the index of the next byte to WRITE. When full, tail wraps
	// modulo capacity and Append starts overwriting the oldest bytes.
	tail int
	full bool

	// totalWritten is a monotonically-increasing count of ALL bytes appended
	// since construction (never wraps). Callers use it to compute the
	// starting-offset a subscriber sees when they subscribe mid-stream:
	//   snapshotStartOffset = totalWritten - len(snapshot)
	// which lets the tailer dedupe on reopen (skip bytes we already emitted).
	totalWritten int64

	// overflowed becomes true the first time an Append pushed us past
	// capacity — i.e. we dropped older bytes. Replay markers use this so
	// a late joiner sees "…[truncated]…" hints rather than assuming they
	// got the whole log from byte 0.
	overflowed bool
}

// newRingBuffer builds an empty ring with the given byte capacity.
// capacity ≤ 0 is coerced to 1 so Append never divides by zero.
func newRingBuffer(capacity int) *ringBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &ringBuffer{capacity: capacity, buf: make([]byte, 0, capacity)}
}

// Append copies p into the ring. Old bytes get overwritten when capacity is
// exceeded. Returns the total number of bytes ever appended (post-append).
func (r *ringBuffer) Append(p []byte) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		r.appendByte(b)
	}
	r.totalWritten += int64(len(p))
	return r.totalWritten
}

func (r *ringBuffer) appendByte(b byte) {
	if !r.full {
		r.buf = append(r.buf, b)
		if len(r.buf) == r.capacity {
			r.full = true
			r.tail = 0
		}
		return
	}
	// full → overwrite at tail.
	r.buf[r.tail] = b
	r.tail = (r.tail + 1) % r.capacity
	r.overflowed = true
}

// Snapshot returns a copy of the current ring contents in chronological order
// (oldest byte first), plus the starting offset (totalWritten - len(snapshot))
// and whether the ring has ever overflowed. The copy is independent — callers
// can hold onto it after subsequent Appends.
func (r *ringBuffer) Snapshot() (bytes []byte, startOffset int64, overflowed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.buf)
	if n == 0 {
		return nil, r.totalWritten, r.overflowed
	}
	out := make([]byte, n)
	if !r.full {
		copy(out, r.buf)
	} else {
		// Bytes from tail..end are the oldest; then 0..tail are the newest.
		copy(out, r.buf[r.tail:])
		copy(out[r.capacity-r.tail:], r.buf[:r.tail])
	}
	return out, r.totalWritten - int64(n), r.overflowed
}

// TotalWritten returns the monotonic counter of bytes ever appended.
// Independent of capacity — useful for dedup on tailer reopen.
func (r *ringBuffer) TotalWritten() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalWritten
}
