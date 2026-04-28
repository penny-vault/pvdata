// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package web

import "time"

// ProgressThrottle invokes emit at most once per interval. The
// first Tick fires immediately; later Ticks within the interval
// are dropped but the most recent count is remembered so Flush
// can emit it later. Not safe for concurrent Ticks.
type ProgressThrottle struct {
	interval time.Duration
	emit     func(int)
	lastSent time.Time
	pending  int
	hasFirst bool
	dirty    bool
}

// NewProgressThrottle returns a throttle that calls emit at most
// once per interval.
func NewProgressThrottle(interval time.Duration, emit func(int)) *ProgressThrottle {
	return &ProgressThrottle{interval: interval, emit: emit}
}

// Tick records the latest count. If the interval has elapsed
// since the last emit (or this is the first tick), emit fires
// immediately.
func (p *ProgressThrottle) Tick(now time.Time, count int) {
	p.pending = count
	p.dirty = true

	if !p.hasFirst || now.Sub(p.lastSent) >= p.interval {
		p.emit(count)
		p.lastSent = now
		p.hasFirst = true
		p.dirty = false
	}
}

// Flush emits the most recent pending count even if the interval
// has not elapsed. Safe to call when nothing is pending; in that
// case it is a no-op.
func (p *ProgressThrottle) Flush() {
	if !p.dirty {
		return
	}

	p.emit(p.pending)
	p.dirty = false
}
