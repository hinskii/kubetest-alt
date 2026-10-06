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

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/storage"
)

var tBase = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// at is the kubelet timestamp of the n-th test second (+ns nanoseconds).
func at(sec int, ns int) time.Time {
	return tBase.Add(time.Duration(sec)*time.Second + time.Duration(ns))
}

// kline renders one kubelet log line: "<RFC 3339 nano> <text>\n".
func kline(ts time.Time, text string) string {
	return ts.Format(time.RFC3339Nano) + " " + text + "\n"
}

// errConnReset is a retriable stream error (anything but io.EOF).
var errConnReset = errors.New("connection reset")

// scriptedStream yields body, then err (io.EOF when nil).
type scriptedStream struct {
	r   io.Reader
	err error
}

func (s *scriptedStream) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if errors.Is(err, io.EOF) {
		if s.err != nil {
			return n, s.err
		}
	}
	return n, err
}
func (s *scriptedStream) Close() error { return nil }

// scriptedSource hands out one scripted stream per Open, recording the
// since argument of each call.
type scriptedSource struct {
	mu      sync.Mutex
	streams []*scriptedStream
	sinces  []*time.Time
}

func (s *scriptedSource) add(err error, lines ...string) *scriptedSource {
	s.streams = append(s.streams, &scriptedStream{r: strings.NewReader(strings.Join(lines, "")), err: err})
	return s
}

func (s *scriptedSource) open(_ context.Context, since *time.Time) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sinces = append(s.sinces, since)
	if len(s.streams) == 0 {
		return nil, errors.New("no more streams")
	}
	next := s.streams[0]
	s.streams = s.streams[1:]
	return next, nil
}

func (s *scriptedSource) openedSince() []*time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*time.Time(nil), s.sinces...)
}

// runToEnd runs a tailer over src until the last stream's EOF and returns
// everything it published, plus its uploader's objects.
func runToEnd(t *testing.T, cfg Config) (string, *captureUploader) {
	t.Helper()
	up := &captureUploader{}
	if cfg.Uploader == nil {
		cfg.Uploader = up
	} else {
		up = cfg.Uploader.(*captureUploader)
	}
	cfg.Bucket = "b"
	cfg.ReopenBackoff = 1
	tl := New(cfg)
	sub := tl.Subscribe()
	tl.Start(t.Context())
	got, reason := drainAll(sub)
	<-tl.Done()
	assert.Equal(t, ReasonEOF, reason)
	return string(got), up
}

func TestTailer_StripsTimestamps(t *testing.T) {
	src := (&scriptedSource{}).add(nil, kline(at(1, 0), "hello"), kline(at(2, 0), "world"))
	got, up := runToEnd(t, Config{Keys: storage.ForRun("ns", "u1"), OpenSource: src.open})
	assert.Equal(t, "hello\nworld\n", got)
	keys, bodies := up.snapshot()
	require.Contains(t, keys, storage.ForRun("ns", "u1").LogChunk(0))
	assert.Equal(t, "hello\nworld\n", string(bodies[0]), "stored chunks carry no kubelet timestamps")
	assert.Nil(t, src.openedSince()[0], "a fresh tailer reads from the first line")
}

// fixes.md #14: after a reconnect the old byte-offset skip dropped real
// lines when the new stream wasn't byte-identical (kubelet log rotation).
// The timestamp position drops exactly the replayed lines.
func TestTailer_ReconnectAfterRotation_KeepsNewLines(t *testing.T) {
	src := (&scriptedSource{}).
		add(errConnReset, kline(at(1, 0), "one"), kline(at(2, 0), "two"), kline(at(3, 0), "a long third line")).
		// Rotated: the new stream starts later and is far shorter than the
		// bytes already published; a byte skip would have eaten "four".
		add(nil, kline(at(3, 0), "a long third line"), kline(at(4, 0), "four"))
	got, _ := runToEnd(t, Config{Keys: storage.ForRun("ns", "u2"), OpenSource: src.open})
	assert.Equal(t, "one\ntwo\na long third line\nfour\n", got)

	sinces := src.openedSince()
	require.Len(t, sinces, 2)
	require.NotNil(t, sinces[1])
	assert.True(t, sinces[1].Equal(at(3, 0)), "reopened at the last published line")
}

// Kubelet timestamps can repeat; replayed lines at the position's exact
// timestamp are dropped by count, later ones at that timestamp are kept.
func TestTailer_ReconnectWithRepeatedTimestamps(t *testing.T) {
	ts := at(5, 123)
	src := (&scriptedSource{}).
		add(errConnReset, kline(ts, "x"), kline(ts, "y")).
		add(nil, kline(at(4, 0), "older"), kline(ts, "x"), kline(ts, "y"), kline(ts, "z"), kline(at(6, 0), "next"))
	got, _ := runToEnd(t, Config{Keys: storage.ForRun("ns", "u3"), OpenSource: src.open})
	assert.Equal(t, "x\ny\nz\nnext\n", got)
}

// fixes.md #13: the reopen budget counted every failure in the run's
// lifetime, so a long run with occasional disconnects stopped tailing for
// good. It now counts failures in a row.
func TestTailer_ReopenBudgetResetsAfterData(t *testing.T) {
	src := &scriptedSource{}
	var want strings.Builder
	for i := range 5 {
		want.WriteString("line\n")
		src.add(errConnReset, kline(at(i, 0), "line"))
	}
	src.add(nil)
	got, _ := runToEnd(t, Config{
		Keys: storage.ForRun("ns", "u4"), OpenSource: src.open, ReopenMaxAttempts: 2,
	})
	assert.Equal(t, want.String(), got, "5 disconnects with data in between must not exhaust a budget of 2")
}

func TestTailer_CursorAfterEachChunk(t *testing.T) {
	src := (&scriptedSource{}).add(nil, kline(at(1, 0), "a"), kline(at(1, 0), "b"))
	keys := storage.ForRun("ns", "u5")
	_, up := runToEnd(t, Config{Keys: keys, OpenSource: src.open})
	rc, err := up.Get(t.Context(), "b", keys.LogCursor())
	require.NoError(t, err)
	var c Cursor
	require.NoError(t, json.NewDecoder(rc).Decode(&c))
	assert.Equal(t, uint64(1), c.NextSeq)
	assert.True(t, c.Last.Equal(at(1, 0)))
	assert.Equal(t, 2, c.AtLast)
}

// fixes.md #12: a restarted operator used to wipe the stored log and read
// the pod from byte 0 (losing what the kubelet had rotated away). It now
// continues the chunk numbering at the stored cursor and skips what was
// already stored.
func TestRegistry_RestartResumesFromCursor(t *testing.T) {
	keys := storage.ForRun("ns", "run-R")
	up := &captureUploader{}
	up.preloadChunk(keys.LogChunk(0), []byte("one\n"))
	up.preloadChunk(keys.LogChunk(1), []byte("two\n"))
	cur, err := json.Marshal(Cursor{NextSeq: 2, Position: Position{Last: at(2, 0), AtLast: 1}})
	require.NoError(t, err)
	up.preloadChunk(keys.LogCursor(), cur)

	fr := newFakeReader()
	src := &fakePodSource{reader: fr}
	reg := NewRegistry(src, up, up, up, "logs")
	require.NoError(t, reg.EnsureTailer(t.Context(), "run-R", keys, "ns", "pod-R"))
	tl := reg.Get("run-R")
	<-tl.Started()
	sub := tl.Subscribe()

	fr.Push([]byte(kline(at(1, 0), "one") + kline(at(2, 0), "two") + kline(at(3, 0), "three")))
	_ = fr.Close()
	got, _ := drainAll(sub) // the stream's EOF ends the tailer
	assert.Equal(t, "three\n", string(got), "replayed lines are not published again")
	reg.StopTailer("run-R")

	assert.Empty(t, up.removedPrefixes(), "a stored log with a cursor is resumed, not wiped")
	sinces := src.openedSince()
	require.Len(t, sinces, 1)
	require.NotNil(t, sinces[0])
	assert.True(t, sinces[0].Equal(at(2, 0)))

	stored, bodies := up.snapshot()
	var log strings.Builder
	for i, k := range stored {
		if strings.HasPrefix(k, keys.Logs()) {
			log.Write(bodies[i])
		}
	}
	assert.Equal(t, "one\ntwo\nthree\n", log.String(), "no line lost, none duplicated")
	assert.Contains(t, stored, keys.LogChunk(2), "numbering continues after the stored chunks")
}
