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

// Test file for internal/logstream. See TestMain — goleak covers the whole
// package so every test must fully quiesce (no orphan goroutines).
package logstream

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs goleak after all tests to catch any goroutine that outlived
// its test. §15.4-relevant: log tailing spawns goroutines per pod and
// leaking one hides real bugs.
//
// Starting strict — add explicit ignore options here if a legitimate long-
// lived background goroutine surfaces (client-go metrics, etc.).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeReader is a test io.ReadCloser whose bytes are pushed by the test
// goroutine. Read blocks on a channel, so tests fully control timing (no
// time.Sleep anywhere). Close unblocks any pending Read with io.EOF.
type fakeReader struct {
	mu     sync.Mutex
	chunks chan []byte
	closed chan struct{}
	// readErr, when set before Close, is returned as the terminal Read
	// error instead of io.EOF. Lets us test the reopen path.
	readErr error

	closeCount int
}

func newFakeReader() *fakeReader {
	return &fakeReader{
		chunks: make(chan []byte, 64),
		closed: make(chan struct{}),
	}
}

// Push queues bytes for Read to hand out. Non-blocking — chunks channel is
// generously buffered so tests can preload data.
func (r *fakeReader) Push(b []byte) { r.chunks <- b }

// SetReadErr configures the terminal error surfaced when Close is called.
// Must be called before Close.
func (r *fakeReader) SetReadErr(err error) {
	r.mu.Lock()
	r.readErr = err
	r.mu.Unlock()
}

// Read hands out whatever's on the chunk channel; blocks if empty.
// Returns io.EOF (or configured readErr) once closed AND drained.
func (r *fakeReader) Read(p []byte) (int, error) {
	select {
	case chunk, ok := <-r.chunks:
		if !ok {
			// Channel closed AND drained — return terminal error.
			r.mu.Lock()
			err := r.readErr
			r.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		n := copy(p, chunk)
		if n < len(chunk) {
			// Put the remainder back so a subsequent Read gets it.
			// (Test chunks are always small enough to fit; guard anyway.)
			leftover := append([]byte(nil), chunk[n:]...)
			// Non-blocking put; test controls channel capacity.
			select {
			case r.chunks <- leftover:
			default:
				panic("fakeReader: leftover would block")
			}
		}
		return n, nil
	case <-r.closed:
		// Drain any final chunk before returning EOF.
		select {
		case chunk, ok := <-r.chunks:
			if ok {
				n := copy(p, chunk)
				return n, nil
			}
		default:
		}
		r.mu.Lock()
		err := r.readErr
		r.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
}

// Close unblocks pending Read with EOF and prevents future Push. Idempotent.
func (r *fakeReader) Close() error {
	r.mu.Lock()
	r.closeCount++
	first := r.closeCount == 1
	r.mu.Unlock()
	if first {
		close(r.chunks)
		close(r.closed)
	}
	return nil
}

// drainAll consumes every remaining frame from a Subscription until its
// channel closes, returning the concatenated bytes and the close reason.
// Used by tests to make assertions synchronization-safe: return only after
// the tailer has closed the subscription.
func drainAll(sub *Subscription) ([]byte, string) {
	var out []byte
	for f := range sub.Frames {
		out = append(out, f.Data...)
	}
	return out, sub.Reason()
}

// -----------------------------------------------------------------------------
// Requirement → test mapping (see report at end of session)
// -----------------------------------------------------------------------------

// R1 Fan-out: 3 subscribers receive identical byte streams; late joiner gets
// ring-buffer prefix + live continuation.
func TestFanOut_ThreeSubscribers_ReceiveSameBytes(t *testing.T) {
	fr := newFakeReader()
	tailer := New(Config{
		RunID: "r1",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) {
			return fr, nil
		},
		FlushInterval:   1e6, // 1s — irrelevant; test drives via Stop
		SubscriberQueue: 32,
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	subA := tailer.Subscribe()
	subB := tailer.Subscribe()
	subC := tailer.Subscribe()

	fr.Push([]byte("hello "))
	fr.Push([]byte("world"))
	_ = fr.Close() // signals EOF once drained

	<-tailer.Done()

	for name, sub := range map[string]*Subscription{"a": subA, "b": subB, "c": subC} {
		bytes, reason := drainAll(sub)
		if string(bytes) != "hello world" {
			t.Errorf("sub %s got %q, want %q", name, bytes, "hello world")
		}
		if reason != ReasonEOF {
			t.Errorf("sub %s reason %q, want %q", name, reason, ReasonEOF)
		}
	}
}

// R1b Late joiner gets ring-buffer prefix + live continuation.
func TestFanOut_LateJoiner_GetsReplayThenLive(t *testing.T) {
	fr := newFakeReader()
	tailer := New(Config{
		RunID:           "r1b",
		OpenSource:      func(_ context.Context) (io.ReadCloser, error) { return fr, nil },
		RingBytes:       1024,
		SubscriberQueue: 32,
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	// Early subscriber drives synchronization: we know the first two chunks
	// have been published (and thus added to the ring) once the early
	// subscriber sees them.
	early := tailer.Subscribe()

	fr.Push([]byte("prefix"))
	fr.Push([]byte("_more"))

	// Drain the first N bytes from early to guarantee publish() has run.
	var earlyGot []byte
	for len(earlyGot) < len("prefix_more") {
		f := <-early.Frames
		earlyGot = append(earlyGot, f.Data...)
	}

	// NOW subscribe — should see prefix_more replayed, then continuation.
	late := tailer.Subscribe()
	fr.Push([]byte("|tail"))
	_ = fr.Close()
	<-tailer.Done()

	// Drain both to completion.
	earlyBytes, _ := drainAll(early)
	lateBytes, lateReason := drainAll(late)

	// early got the whole log across both drains
	full := append(earlyGot, earlyBytes...)
	if string(full) != "prefix_more|tail" {
		t.Errorf("early got %q, want %q", full, "prefix_more|tail")
	}

	if string(lateBytes) != "prefix_more|tail" {
		t.Errorf("late got %q, want %q", lateBytes, "prefix_more|tail")
	}
	if lateReason != ReasonEOF {
		t.Errorf("late reason %q, want %q", lateReason, ReasonEOF)
	}
}

// R2 Ring buffer: overflow keeps newest N bytes; replay marker indicates
// truncation.
func TestRing_OverflowKeepsNewestAndMarksReplay(t *testing.T) {
	fr := newFakeReader()
	tailer := New(Config{
		RunID: "r2",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) {
			return fr, nil
		},
		RingBytes:       4, // tiny ring so we overflow immediately
		SubscriberQueue: 32,
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	// Drive publish through a synchronizing early subscriber.
	gate := tailer.Subscribe()

	fr.Push([]byte("AAAA")) // fills the ring exactly
	fr.Push([]byte("BBBB")) // pushes AAAA out
	fr.Push([]byte("CDEF")) // pushes BBBB out; ring now holds "CDEF"

	// Wait for those 12 bytes to have been published.
	var got []byte
	for len(got) < 12 {
		f := <-gate.Frames
		got = append(got, f.Data...)
	}

	// Late subscribe — ring should replay only "CDEF" with truncated marker.
	late := tailer.Subscribe()
	first := <-late.Frames
	if string(first.Data) != "CDEF" {
		t.Errorf("late replay %q, want %q", first.Data, "CDEF")
	}
	if first.Marker != MarkerReplayTruncated {
		t.Errorf("late marker %q, want %q", first.Marker, MarkerReplayTruncated)
	}

	_ = fr.Close()
	<-tailer.Done()
	// Drain both to completion so goleak passes.
	_, _ = drainAll(gate)
	_, _ = drainAll(late)
}

// R3 Slow client: blocked subscriber gets dropped/marked after queue limit;
// other subscribers unaffected. Timing-safe — every byte the fast subscriber
// reads is echoed on a channel the test drains BEFORE pushing the next byte.
// That makes the "did the publish happen?" question a channel-receive, not
// a wall-clock guess.
func TestSlowClient_DroppedWithOverflowMarker(t *testing.T) {
	const queue = 4
	fr := newFakeReader()
	tailer := New(Config{
		RunID:           "r3",
		OpenSource:      func(_ context.Context) (io.ReadCloser, error) { return fr, nil },
		SubscriberQueue: queue,
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	slow := tailer.Subscribe() // never reads
	fast := tailer.Subscribe() // drains eagerly, echoing each byte

	fastEchoes := make(chan byte, 256)
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		for f := range fast.Frames {
			for _, b := range f.Data {
				fastEchoes <- b
			}
		}
	}()

	// Push one byte and wait for fast to have consumed it before pushing
	// the next. After queue+1 = 5 bytes, slow's channel (cap 4) fills up
	// and the next publish drops it with ReasonOverflow. Fast is drained
	// after each push so it never overflows.
	const total = queue + 6 // 10 bytes; slow drops around byte 5
	for i := range total {
		fr.Push([]byte{byte('A' + i)})
		<-fastEchoes
	}

	_ = fr.Close()
	<-tailer.Done()
	<-fastDone

	slowBytes, slowReason := drainAll(slow)
	if slowReason != ReasonOverflow {
		t.Errorf("slow reason %q, want %q (got %d bytes: %q)", slowReason, ReasonOverflow, len(slowBytes), slowBytes)
	}
	// slow got at most `queue` bytes before its channel filled.
	if len(slowBytes) > queue {
		t.Errorf("slow got %d bytes, expected ≤%d before overflow", len(slowBytes), queue)
	}
}

// R4a Flush: chunks uploaded at size threshold.
func TestFlush_ChunkAtSizeThreshold(t *testing.T) {
	fr := newFakeReader()
	up := &captureUploader{}
	tailer := New(Config{
		RunID:      "r4a",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) { return fr, nil },
		Uploader:   up,
		Bucket:     "logs",
		ChunkBytes: 4, // flush every 4 bytes
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	gate := tailer.Subscribe()

	fr.Push([]byte("AB"))   // pending=2, no flush
	fr.Push([]byte("CD"))   // pending=4, flush → chunk 0
	fr.Push([]byte("EFGH")) // pending=4, flush → chunk 1

	// Wait until 8 bytes published so we know both flushes happened.
	var got []byte
	for len(got) < 8 {
		f := <-gate.Frames
		got = append(got, f.Data...)
	}

	_ = fr.Close()
	<-tailer.Done()
	_, _ = drainAll(gate)

	keys, bodies := up.snapshot()
	// Expect at least 2 chunks (final finalize flush may add a 3rd empty
	// one — but flushPending is a no-op when pending is empty).
	if len(keys) < 2 {
		t.Fatalf("expected ≥2 chunks, got keys=%v", keys)
	}
	// Chunk 0 must be "ABCD", chunk 1 must be "EFGH".
	if string(bodies[0]) != "ABCD" {
		t.Errorf("chunk 0 %q, want %q", bodies[0], "ABCD")
	}
	if string(bodies[1]) != "EFGH" {
		t.Errorf("chunk 1 %q, want %q", bodies[1], "EFGH")
	}
	if keys[0] != LogChunkKey("r4a", 0) || keys[1] != LogChunkKey("r4a", 1) {
		t.Errorf("chunk keys unexpected: %v", keys)
	}
}

// R4b Flush: on source EOF final flush contains full remaining tail.
func TestFlush_FinalFlushOnEOF(t *testing.T) {
	fr := newFakeReader()
	up := &captureUploader{}
	tailer := New(Config{
		RunID:      "r4b",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) { return fr, nil },
		Uploader:   up,
		Bucket:     "logs",
		ChunkBytes: 1024, // large — no size-triggered flush
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	gate := tailer.Subscribe()

	fr.Push([]byte("partial-tail"))
	// Drive publish to completion.
	var got []byte
	for len(got) < len("partial-tail") {
		f := <-gate.Frames
		got = append(got, f.Data...)
	}
	_ = fr.Close()
	<-tailer.Done()
	_, _ = drainAll(gate)

	keys, bodies := up.snapshot()
	if len(keys) != 1 {
		t.Fatalf("expected 1 chunk from final flush, got %v", keys)
	}
	if string(bodies[0]) != "partial-tail" {
		t.Errorf("final chunk %q, want %q", bodies[0], "partial-tail")
	}
}

// R4c Simulated stream error mid-run → resume produces no duplicate/lost
// bytes (offset dedupe).
func TestReopen_DedupsAlreadyEmittedBytes(t *testing.T) {
	// Two fake readers: first errors after emitting "AAA"; second replays
	// "AAA" (as k8s would on reopen) then continues with "BBB". The tailer
	// must skip the duplicate "AAA" and deliver only "AAABBB" to subscribers.
	first := newFakeReader()
	second := newFakeReader()
	var callCount int
	var mu sync.Mutex

	tailer := New(Config{
		RunID: "r4c",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			callCount++
			if callCount == 1 {
				return first, nil
			}
			return second, nil
		},
		ReopenBackoff:     1, // 1ns — instant
		ReopenMaxAttempts: 5,
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	gate := tailer.Subscribe()

	// First stream: emit AAA, then close with a NON-EOF error to force reopen.
	first.SetReadErr(errors.New("connection reset"))
	first.Push([]byte("AAA"))
	// Wait for those 3 bytes.
	var got []byte
	for len(got) < 3 {
		f := <-gate.Frames
		got = append(got, f.Data...)
	}
	_ = first.Close()

	// The tailer will now reopen. Feed the second stream a duplicate prefix
	// then new bytes.
	second.Push([]byte("AAA")) // must be deduped
	second.Push([]byte("BBB")) // must be delivered
	_ = second.Close()

	<-tailer.Done()
	rest, reason := drainAll(gate)
	got = append(got, rest...)

	if string(got) != "AAABBB" {
		t.Errorf("subscriber got %q, want %q (dedupe failed)", got, "AAABBB")
	}
	if reason != ReasonEOF {
		t.Errorf("reason %q, want %q", reason, ReasonEOF)
	}
}

// R5 Race safety: concurrent Subscribe/Unsubscribe/publish under -race.
// Runs with `go test -race`; body is designed to trigger data-race detection
// if publish/subscribe/unsubscribe touch shared state without locking.
func TestConcurrent_SubscribeUnsubscribePublish(t *testing.T) {
	fr := newFakeReader()
	tailer := New(Config{
		RunID:           "r5",
		OpenSource:      func(_ context.Context) (io.ReadCloser, error) { return fr, nil },
		SubscriberQueue: 128,
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	// Producer: pump bytes.
	var producerDone sync.WaitGroup
	producerDone.Go(func() {
		for i := range 100 {
			fr.Push([]byte{byte(i % 256)})
		}
		_ = fr.Close()
	})

	// Subscribers: many, briefly alive, drain then unsubscribe.
	var subs sync.WaitGroup
	for range 20 {
		subs.Go(func() {
			s := tailer.Subscribe()
			// Drain a handful of frames then unsubscribe.
			for range 5 {
				select {
				case <-s.Frames:
				case <-tailer.Done():
					// Drain remaining synchronously.
					for range s.Frames {
					}
					return
				}
			}
			tailer.Unsubscribe(s)
			// After Unsubscribe, drain remaining until close.
			for range s.Frames {
			}
		})
	}

	producerDone.Wait()
	<-tailer.Done()
	subs.Wait()
}

// R6 Registry idempotency: EnsureTailer on same runID returns the same
// tailer; StopTailer removes it and flushes.
func TestRegistry_EnsureTailerIsIdempotent(t *testing.T) {
	fr := newFakeReader()
	src := &fakePodSource{reader: fr}
	up := &captureUploader{}
	reg := NewRegistry(src, up, "logs")

	ctx := t.Context()

	if err := reg.EnsureTailer(ctx, "run-A", "ns", "pod-A"); err != nil {
		t.Fatalf("first EnsureTailer: %v", err)
	}
	if err := reg.EnsureTailer(ctx, "run-A", "ns", "pod-A"); err != nil {
		t.Fatalf("second EnsureTailer: %v", err)
	}
	a := reg.Get("run-A")
	b := reg.Get("run-A")
	if a == nil || a != b {
		t.Errorf("Get not idempotent — got %p vs %p", a, b)
	}

	active := reg.Active()
	if len(active) != 1 || active[0] != "run-A" {
		t.Errorf("Active() = %v, want [run-A]", active)
	}

	// Push some bytes so we have something to flush.
	<-a.Started()
	gate := a.Subscribe()
	fr.Push([]byte("hi"))
	for f := range gate.Frames {
		if len(f.Data) >= 2 {
			break
		}
	}
	// StopTailer must block until flush completes.
	reg.StopTailer("run-A")

	if len(reg.Active()) != 0 {
		t.Errorf("StopTailer did not remove entry: %v", reg.Active())
	}
	keys, _ := up.snapshot()
	if len(keys) == 0 {
		t.Errorf("expected final flush after StopTailer, got no keys")
	}
	// Drain sub so goleak passes.
	for range gate.Frames {
	}

	// Shutdown of empty registry is a no-op.
	reg.Shutdown()
	if err := reg.EnsureTailer(ctx, "run-B", "ns", "pod-B"); !errors.Is(err, ErrRegistryClosed) {
		t.Errorf("after Shutdown expected ErrRegistryClosed, got %v", err)
	}
}

// R7 Registry Shutdown stops every tailer.
func TestRegistry_ShutdownStopsAllTailers(t *testing.T) {
	src := &fakePodSource{makeReader: newFakeReader}
	reg := NewRegistry(src, nil, "")

	ctx := t.Context()
	if err := reg.EnsureTailer(ctx, "run-A", "ns", "pod-A"); err != nil {
		t.Fatalf("EnsureTailer A: %v", err)
	}
	if err := reg.EnsureTailer(ctx, "run-B", "ns", "pod-B"); err != nil {
		t.Fatalf("EnsureTailer B: %v", err)
	}
	tA := reg.Get("run-A")
	tB := reg.Get("run-B")
	<-tA.Started()
	<-tB.Started()

	reg.Shutdown()

	select {
	case <-tA.Done():
	default:
		t.Errorf("Shutdown didn't wait for tailer A")
	}
	select {
	case <-tB.Done():
	default:
		t.Errorf("Shutdown didn't wait for tailer B")
	}
}

// R8 Subscribe after Stop returns a pre-closed subscription with ReasonEOF.
func TestSubscribe_AfterStop_ReturnsClosed(t *testing.T) {
	fr := newFakeReader()
	tailer := New(Config{
		RunID:      "r8",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) { return fr, nil },
	})
	tailer.Start(t.Context())
	<-tailer.Started()
	_ = fr.Close()
	<-tailer.Done()

	late := tailer.Subscribe()
	// Frames should already be closed.
	select {
	case _, ok := <-late.Frames:
		if ok {
			t.Errorf("expected closed Frames channel")
		}
	default:
		t.Errorf("Frames should be pre-closed")
	}
	if late.Reason() != ReasonEOF {
		t.Errorf("reason %q, want %q", late.Reason(), ReasonEOF)
	}
}

// R9 Unsubscribe closes with ReasonUnsubscribed and stops delivery.
func TestUnsubscribe_ClosesWithReason(t *testing.T) {
	fr := newFakeReader()
	tailer := New(Config{
		RunID:      "r9",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) { return fr, nil },
	})
	tailer.Start(t.Context())
	<-tailer.Started()

	sub := tailer.Subscribe()
	tailer.Unsubscribe(sub)

	// Channel must be closed.
	_, ok := <-sub.Frames
	if ok {
		t.Errorf("Frames not closed after Unsubscribe")
	}
	if sub.Reason() != ReasonUnsubscribed {
		t.Errorf("reason %q, want %q", sub.Reason(), ReasonUnsubscribed)
	}

	// Second Unsubscribe is a no-op.
	tailer.Unsubscribe(sub)

	// Cleanup.
	_ = fr.Close()
	<-tailer.Done()
}

// R10 Reopen budget exhausted → tailer exits cleanly.
func TestReopen_BudgetExhausted(t *testing.T) {
	openErr := errors.New("open failed")
	tailer := New(Config{
		RunID: "r10",
		OpenSource: func(_ context.Context) (io.ReadCloser, error) {
			return nil, openErr
		},
		ReopenMaxAttempts: 2,
		ReopenBackoff:     1,
	})
	tailer.Start(t.Context())
	<-tailer.Done() // must exit on its own without hanging

	sub := tailer.Subscribe()
	if sub.Reason() != ReasonEOF {
		t.Errorf("reason %q, want %q", sub.Reason(), ReasonEOF)
	}
}

// R11 Ring buffer unit tests (exhaustive coverage of ring.go).
func TestRingBuffer_Basics(t *testing.T) {
	r := newRingBuffer(4)
	r.Append([]byte("AB"))
	snap, off, over := r.Snapshot()
	if string(snap) != "AB" || off != 0 || over {
		t.Errorf("state after AB: snap=%q off=%d over=%v", snap, off, over)
	}
	r.Append([]byte("CD"))
	snap, off, over = r.Snapshot()
	if string(snap) != "ABCD" || off != 0 || over {
		t.Errorf("state after ABCD: snap=%q off=%d over=%v", snap, off, over)
	}
	r.Append([]byte("EF"))
	snap, off, over = r.Snapshot()
	if string(snap) != "CDEF" || off != 2 || !over {
		t.Errorf("state after EF: snap=%q off=%d over=%v", snap, off, over)
	}
	if got := r.TotalWritten(); got != 6 {
		t.Errorf("TotalWritten=%d, want 6", got)
	}
}

func TestRingBuffer_EmptySnapshot(t *testing.T) {
	r := newRingBuffer(4)
	snap, _, over := r.Snapshot()
	if snap != nil || over {
		t.Errorf("empty ring: snap=%v over=%v", snap, over)
	}
}

func TestRingBuffer_CapacityCoercion(t *testing.T) {
	r := newRingBuffer(0)
	r.Append([]byte("ABC"))
	snap, _, over := r.Snapshot()
	if len(snap) != 1 || !over {
		t.Errorf("capacity=0 coerced: snap=%q over=%v", snap, over)
	}
}

// R12 LogChunkKey format is stable.
func TestLogChunkKey_Format(t *testing.T) {
	if k := LogChunkKey("run-x", 0); k != "kubetest-logs/run-x/00000000.log" {
		t.Errorf("chunk 0 key = %q", k)
	}
	if k := LogChunkKey("run-x", 42); k != "kubetest-logs/run-x/00000042.log" {
		t.Errorf("chunk 42 key = %q", k)
	}
}

// -----------------------------------------------------------------------------
// Test helpers below this line — no test functions.
// -----------------------------------------------------------------------------

// captureUploader is a synchronous, in-memory Uploader that records every
// Put call in the order it happened. Used by flush tests to assert chunk
// keys and payloads.
type captureUploader struct {
	mu     sync.Mutex
	keys   []string
	bodies [][]byte
}

func (c *captureUploader) Put(_ context.Context, _bucket, key string,
	r io.Reader, _size int64, _contentType string) error {

	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = append(c.keys, key)
	c.bodies = append(c.bodies, body)
	return nil
}

func (c *captureUploader) snapshot() ([]string, [][]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := append([]string(nil), c.keys...)
	bodies := make([][]byte, len(c.bodies))
	for i, b := range c.bodies {
		bodies[i] = append([]byte(nil), b...)
	}
	return keys, bodies
}

// fakePodSource is a PodLogSource that hands out a fake reader per Open.
// If `reader` is set, that single reader is returned on every Open; else
// `makeReader` is called to build a fresh one each time.
type fakePodSource struct {
	reader     *fakeReader
	makeReader func() *fakeReader
}

func (s *fakePodSource) Open(_ context.Context, _ns, _pod string) (io.ReadCloser, error) {
	if s.reader != nil {
		return s.reader, nil
	}
	return s.makeReader(), nil
}
