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

import "time"

// # Position: where in the pod's log a tailer is
//
// Pod logs are read with timestamps=true: the kubelet prefixes every line
// with its RFC 3339 (nanosecond) timestamp and a space. The tailer strips
// the prefix — stored and streamed logs look exactly like the container's
// output — and remembers the timestamp of the last line it published plus
// how many lines carried that same timestamp.
//
// That position survives what a byte offset can't (fixes.md #14): after a
// reconnect the stream is reopened with sinceTime=Last and every line at
// or before the position is dropped, whatever the kubelet's log rotation
// did to the files in between. Lines lost to rotation stay lost; real new
// lines are never skipped. The same position, saved next to the chunks
// as Cursor, lets a restarted operator resume (fixes.md #12).

// Position is the last published line: its timestamp, and how many lines
// in a row carried exactly that timestamp (kubelet timestamps can repeat).
type Position struct {
	Last   time.Time `json:"last"`
	AtLast int       `json:"atLast"`
}

// advance records a published line with timestamp ts.
func (p *Position) advance(ts time.Time) {
	if ts.Equal(p.Last) {
		p.AtLast++
		return
	}
	p.Last, p.AtLast = ts, 1
}

// Cursor is what the tailer persists after each stored chunk.
type Cursor struct {
	// NextSeq is the chunk number the next flush writes.
	NextSeq uint64 `json:"nextSeq"`
	Position
}

// replayFilter drops the lines of a reopened stream that were already
// published. One per source connection.
type replayFilter struct {
	upTo Position // published up to here before this connection
	seen int      // lines seen at exactly upTo.Last on this connection
}

// keep reports whether a line with timestamp ts is new.
func (f *replayFilter) keep(ts time.Time) bool {
	switch {
	case ts.Before(f.upTo.Last):
		return false
	case ts.Equal(f.upTo.Last):
		f.seen++
		return f.seen > f.upTo.AtLast
	}
	return true
}
