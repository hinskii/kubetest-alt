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

// Package logstream tails a pod's stdout, fans it out to subscribers, and
// flushes chunks to object storage continuously. One Tailer per active run;
// a Registry owns the map keyed by runID for the controller.
//
// # Flush strategy: chunk-objects (not rolling rewrite)
//
// Chunks land at:
//
//	<bucket>/kubetest-logs/<runID>/00000000.log
//	<bucket>/kubetest-logs/<runID>/00000001.log
//	<bucket>/kubetest-logs/<runID>/...
//
// Zero-padded eight-digit suffixes so lexicographic listing reproduces
// chronological order. We reject the "one growing object rewritten on each
// flush" alternative: S3-family stores have no append primitive, so growing
// an object means re-uploading its full contents each flush — for a run
// producing N bytes across k flushes that's O(k·N) upload volume. A 10MB
// log flushed 100 times uploads ~500MB. Chunk-objects are O(N).
//
// # Concurrency shape
//
// One goroutine per Tailer: the run loop. It owns the source ReadCloser,
// the flush timer, the pending chunk buffer, and the "have we hit EOF?"
// flag. Subscribers publish/subscribe from arbitrary goroutines behind
// subsMu. A short-lived pump goroutine runs during each source's lifetime
// only, so its exit is coupled to source Close (the Stop path forces that).
//
// # Backpressure
//
// Fan-out uses non-blocking sends into per-subscriber bounded channels.
// A subscriber whose channel is full is dropped: the tailer closes their
// channel with Reason() == ReasonOverflow. The tailer NEVER blocks on a
// subscriber — that's the whole point of the drop policy.
package logstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hinskii/kubetest-alt/internal/metrics"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// Defaults tuned for typical test-runner output. All override-able via Config.
const (
	DefaultRingBytes         = 64 * 1024 // 64KB replay buffer per §12
	DefaultChunkBytes        = 512 * 1024
	DefaultFlushInterval     = 5 * time.Second
	DefaultSubscriberQueue   = 32
	DefaultReadBufferBytes   = 4 * 1024
	DefaultReopenMaxAttempts = 5
	DefaultReopenBackoff     = 200 * time.Millisecond
)

// Reason constants — set on a Subscription when its Frames channel closes
// so subscribers can tell "you got the whole log" from "you were too slow"
// from "you unsubscribed yourself."
const (
	ReasonEOF          = "eof"
	ReasonOverflow     = "overflow"
	ReasonUnsubscribed = "unsubscribed"
)

// LogChunkKey returns the object-store key for a given run + chunk sequence.
// Exported so the API server can construct the same paths without importing
// package internals. Prefix comes from LogPrefix so restart-wipe and
// chunk-write always target the same path.
func LogChunkKey(runID string, seq uint64) string {
	return fmt.Sprintf("%s%08d.log", LogPrefix(runID), seq)
}

// Config is the Tailer constructor input. Zero fields pick defaults; RunID
// and OpenSource are required.
type Config struct {
	// RunID becomes the key prefix in object storage.
	RunID string

	// OpenSource returns a fresh follow-mode reader over the pod's stdout.
	// The Tailer calls it at Start and again after each retriable read
	// error (up to ReopenMaxAttempts). Required.
	OpenSource func(ctx context.Context) (io.ReadCloser, error)

	// Uploader receives chunks. Nil disables the flush path — useful for
	// tests that exercise only fan-out.
	Uploader storage.Uploader

	// Bucket for chunk uploads. Ignored when Uploader is nil.
	Bucket string

	// Tunables — 0 means "use the Default* constant."
	RingBytes         int
	ChunkBytes        int
	FlushInterval     time.Duration
	SubscriberQueue   int
	ReadBufferBytes   int
	ReopenMaxAttempts int
	ReopenBackoff     time.Duration
}

// Frame is what subscribers receive on their Frames channel. Data holds
// bytes read from the source; Marker is non-empty only on the initial
// replay frame when the ring had already overflowed by Subscribe time.
type Frame struct {
	Data   []byte
	Marker string
}

const (
	// MarkerReplayTruncated is set on a subscriber's FIRST frame when the
	// ring buffer had already overflowed at Subscribe time — the snapshot
	// they received is not the full log-from-byte-0. Live frames afterward
	// are complete.
	MarkerReplayTruncated = "replay-truncated"
)

// Subscription is what Subscribe returns. Callers range over Frames until
// it closes, then call Reason() to learn why.
type Subscription struct {
	Frames <-chan Frame

	id     int
	frames chan Frame
	reason atomic.Value // stores string; "" until closed
	once   sync.Once
}

// Reason returns the close reason after Frames closes. Returns "" if the
// channel is still open. Values: ReasonEOF, ReasonOverflow, ReasonUnsubscribed.
func (s *Subscription) Reason() string {
	v := s.reason.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// close is idempotent. Called by the tailer (with EOF/overflow) or by
// Unsubscribe. The first caller wins the reason.
func (s *Subscription) close(reason string) {
	s.once.Do(func() {
		s.reason.Store(reason)
		close(s.frames)
	})
}

// Tailer streams one pod's logs. Not safe to reuse after Stop.
type Tailer struct {
	cfg Config

	ring *ringBuffer

	// Subscribers. subsMu is held during Subscribe AND during each publish,
	// so a subscriber never misses frames between "snapshot taken" and
	// "registered."
	subsMu    sync.Mutex
	subs      map[int]*Subscription
	nextSubID int
	stopped   bool // set true after run loop exits; Subscribe returns pre-closed sub

	// Pending chunk buffer — owned solely by the run loop, so no lock.
	pending  bytes.Buffer
	chunkSeq uint64

	// Total bytes handed to publish so far. Used as the skip offset on
	// reopen — the next stream re-emits from byte 0, and we drop the prefix
	// we've already delivered.
	emitted int64

	// Lifecycle.
	started   chan struct{}
	done      chan struct{}
	cancel    context.CancelFunc
	stopOnce  sync.Once
	startOnce sync.Once
}

// New constructs a Tailer. Does not start any goroutine — call Start.
func New(cfg Config) *Tailer {
	applyDefaults(&cfg)
	return &Tailer{
		cfg:     cfg,
		ring:    newRingBuffer(cfg.RingBytes),
		subs:    map[int]*Subscription{},
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func applyDefaults(cfg *Config) {
	if cfg.RingBytes == 0 {
		cfg.RingBytes = DefaultRingBytes
	}
	if cfg.ChunkBytes == 0 {
		cfg.ChunkBytes = DefaultChunkBytes
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = DefaultFlushInterval
	}
	if cfg.SubscriberQueue == 0 {
		cfg.SubscriberQueue = DefaultSubscriberQueue
	}
	if cfg.ReadBufferBytes == 0 {
		cfg.ReadBufferBytes = DefaultReadBufferBytes
	}
	if cfg.ReopenMaxAttempts == 0 {
		cfg.ReopenMaxAttempts = DefaultReopenMaxAttempts
	}
	if cfg.ReopenBackoff == 0 {
		cfg.ReopenBackoff = DefaultReopenBackoff
	}
}

// Start spawns the run goroutine. Safe to call once; subsequent calls no-op.
func (t *Tailer) Start(ctx context.Context) {
	t.startOnce.Do(func() {
		innerCtx, cancel := context.WithCancel(ctx)
		t.cancel = cancel
		go t.run(innerCtx)
	})
}

// Started returns a channel closed once the run loop has attempted its
// first source Open and entered the select loop (or exited early). Test
// hook — production callers don't need to wait.
func (t *Tailer) Started() <-chan struct{} { return t.started }

// Done returns a channel closed when the run loop exits.
func (t *Tailer) Done() <-chan struct{} { return t.done }

// Stop cancels the run loop and blocks until it exits. Safe from any
// goroutine and idempotent.
func (t *Tailer) Stop() {
	t.stopOnce.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}
	})
	<-t.done
}

// Subscribe adds a subscriber. The first frame delivered is a snapshot of
// the ring buffer (skipped if the ring is empty); subsequent frames are
// live tail. If the tailer has already stopped, returns a Subscription
// whose Frames is already closed with ReasonEOF.
func (t *Tailer) Subscribe() *Subscription {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()

	frames := make(chan Frame, t.cfg.SubscriberQueue)
	sub := &Subscription{
		Frames: frames,
		frames: frames,
		id:     t.nextSubID,
	}
	t.nextSubID++

	if t.stopped {
		sub.close(ReasonEOF)
		return sub
	}

	snap, _, overflowed := t.ring.Snapshot()
	if len(snap) > 0 {
		marker := ""
		if overflowed {
			marker = MarkerReplayTruncated
		}
		// Cap ≥ 1 by applyDefaults, empty channel — send never blocks.
		frames <- Frame{Data: snap, Marker: marker}
	}
	t.subs[sub.id] = sub
	return sub
}

// Unsubscribe removes a subscription and closes its Frames channel with
// ReasonUnsubscribed. Safe to call multiple times.
func (t *Tailer) Unsubscribe(sub *Subscription) {
	if sub == nil {
		return
	}
	t.subsMu.Lock()
	_, present := t.subs[sub.id]
	if present {
		delete(t.subs, sub.id)
	}
	t.subsMu.Unlock()
	if present {
		sub.close(ReasonUnsubscribed)
	}
}

// publish appends bytes to ring + pending buffer, then fans out to
// subscribers. Non-blocking per subscriber: full queue → drop with
// ReasonOverflow. Called only from the run loop.
func (t *Tailer) publish(data []byte) {
	if len(data) == 0 {
		return
	}
	t.ring.Append(data)
	t.pending.Write(data)
	t.emitted += int64(len(data))

	// Copy so subscribers don't share the run loop's read buffer.
	frame := Frame{Data: append([]byte(nil), data...)}

	t.subsMu.Lock()
	var dropped []*Subscription
	for id, sub := range t.subs {
		select {
		case sub.frames <- frame:
		default:
			delete(t.subs, id)
			dropped = append(dropped, sub)
		}
	}
	t.subsMu.Unlock()

	for _, sub := range dropped {
		sub.close(ReasonOverflow)
	}
}

// readEvent is what the pump goroutine sends to the run loop.
type readEvent struct {
	data      []byte
	err       error
	retriable bool // false only for io.EOF
}

// run is the single tailer goroutine. Owns:
//   - source open + reopen loop with byte-offset dedup
//   - flush timer
//   - final flush + subscriber notification at exit
func (t *Tailer) run(ctx context.Context) {
	// Order matters: finalize (flush + close subs) BEFORE we close done —
	// callers waiting on Done() then see a fully drained tailer.
	defer close(t.done)
	defer t.finalize()

	// Signal Started once, exactly once, no matter which path we take.
	startedOnce := sync.Once{}
	signalStarted := func() { startedOnce.Do(func() { close(t.started) }) }

	timer := time.NewTicker(t.cfg.FlushInterval)
	defer timer.Stop()

	readCh := make(chan readEvent, 1)
	attempts := 0

	var currentSrc io.ReadCloser
	var pumpDone chan struct{}

	// startPump opens a fresh source and spawns the read goroutine. skip
	// is the number of bytes to discard from the head of this stream (the
	// portion we already delivered on a prior connection). Returns false
	// if opening failed and we've hit the retry budget or ctx cancelled.
	startPump := func(skip int64) bool {
		for {
			if ctx.Err() != nil {
				return false
			}
			src, err := t.cfg.OpenSource(ctx)
			if err == nil {
				currentSrc = src
				done := make(chan struct{})
				pumpDone = done
				go pump(ctx, src, skip, t.cfg.ReadBufferBytes, readCh, done)
				return true
			}
			attempts++
			if attempts >= t.cfg.ReopenMaxAttempts {
				return false
			}
			if !sleepCtx(ctx, t.cfg.ReopenBackoff) {
				return false
			}
		}
	}

	if !startPump(0) {
		signalStarted()
		return
	}
	signalStarted()

	for {
		select {
		case <-ctx.Done():
			// Force pump out of its blocking Read.
			if currentSrc != nil {
				_ = currentSrc.Close()
			}
			if pumpDone != nil {
				<-pumpDone
			}
			return

		case <-timer.C:
			t.flushPending(ctx)

		case ev := <-readCh:
			if len(ev.data) > 0 {
				t.publish(ev.data)
				if t.pending.Len() >= t.cfg.ChunkBytes {
					t.flushPending(ctx)
				}
			}
			if ev.err == nil {
				continue
			}
			// Pump has already terminated by contract (it exits after
			// sending an event carrying err != nil). Await its done so we
			// don't race the next startPump.
			<-pumpDone
			_ = currentSrc.Close()
			currentSrc = nil
			pumpDone = nil

			if !ev.retriable {
				// EOF — clean exit.
				return
			}

			attempts++
			if attempts >= t.cfg.ReopenMaxAttempts {
				return
			}
			if !sleepCtx(ctx, t.cfg.ReopenBackoff) {
				return
			}
			if !startPump(t.emitted) {
				return
			}
		}
	}
}

// pump reads from src until EOF/error or ctx cancellation and forwards
// results into readCh. Runs in its own goroutine — one per source
// connection. Always closes done on exit.
//
// Under ctx cancel the run loop closes src, which unblocks the Read call;
// we then send the resulting error (or fall through the readCh send's ctx
// arm if the run loop is already gone).
func pump(ctx context.Context, src io.Reader, skip int64,
	bufSize int, readCh chan<- readEvent, done chan<- struct{}) {

	defer close(done)
	buf := make([]byte, bufSize)

	for {
		n, err := src.Read(buf)
		if n > 0 {
			slice := buf[:n]
			if skip > 0 {
				if int64(n) <= skip {
					skip -= int64(n)
					slice = nil
				} else {
					slice = slice[skip:]
					skip = 0
				}
			}
			if len(slice) > 0 {
				data := append([]byte(nil), slice...)
				select {
				case readCh <- readEvent{data: data}:
				case <-ctx.Done():
					return
				}
			}
		}
		if err == nil {
			continue
		}
		terminal := readEvent{err: err, retriable: !errors.Is(err, io.EOF)}
		select {
		case readCh <- terminal:
		case <-ctx.Done():
		}
		return
	}
}

// flushPending uploads whatever's in the pending buffer as the next chunk,
// then clears the buffer. Called from the run loop only.
//
// On upload error we still increment chunkSeq and clear pending — trading
// log durability for progress. Blocking the tail loop on MinIO would defeat
// the subscriber-backpressure guarantee, and the ring buffer + live
// subscribers still see everything. Repeated failures produce visible gaps
// in the MinIO layout, which is exactly the signal SRE needs.
func (t *Tailer) flushPending(ctx context.Context) {
	if t.pending.Len() == 0 || t.cfg.Uploader == nil {
		return
	}
	body := append([]byte(nil), t.pending.Bytes()...)
	key := LogChunkKey(t.cfg.RunID, t.chunkSeq)
	if err := t.cfg.Uploader.Put(ctx, t.cfg.Bucket, key, bytes.NewReader(body),
		int64(len(body)), "text/plain"); err == nil {
		// Only count successful uploads — a failed Put doesn't move
		// bytes to storage.
		metrics.LogStreamBytesTotal.Add(float64(len(body)))
	}
	t.chunkSeq++
	t.pending.Reset()
}

// finalize runs on run-loop exit: last flush, close all subscribers with
// ReasonEOF, mark stopped so Subscribe returns pre-closed subs. Uses a
// fresh background ctx so the flush still happens even when Stop cancelled
// the tailer's own ctx — otherwise Stop would kill the final MinIO write.
func (t *Tailer) finalize() {
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.flushPending(flushCtx)

	t.subsMu.Lock()
	t.stopped = true
	subs := make([]*Subscription, 0, len(t.subs))
	for id, sub := range t.subs {
		subs = append(subs, sub)
		delete(t.subs, id)
	}
	t.subsMu.Unlock()

	for _, sub := range subs {
		sub.close(ReasonEOF)
	}
}

// sleepCtx sleeps for d unless ctx is cancelled first. Returns true if the
// full sleep completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
