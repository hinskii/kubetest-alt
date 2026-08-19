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

package scheduler

import (
	"sync"
	"time"
)

// Clock is the injection point for time. Production wires RealClock; tests
// wire FakeClock so no assertion depends on the wall clock.
//
// Shared between the cron scheduler and the trigger gate manager so both
// pieces move in lock-step under the same fake clock.
type Clock interface {
	Now() time.Time
}

// RealClock returns wall time.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a hand-driven clock for tests. Set advances "now" verbatim;
// there are no goroutines, no channels, no time.Sleep anywhere in this file.
// Tests advance the clock, then call Scheduler.Tick / gateManager.Evaluate
// directly to observe the effect.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock seeds the clock at the given time.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

// Now implements Clock.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set jumps the clock to t. Callers pick absolute times so tests read cleanly
// ("advance to next cron boundary at 2026-01-01T00:05:00Z"), rather than
// chasing accumulated deltas.
func (f *FakeClock) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the clock forward by d. Kept for tests that read in terms of
// deltas rather than absolute times.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
