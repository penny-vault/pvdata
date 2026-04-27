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

import (
	"sync"
	"time"
)

// sseEvent represents a single Server-Sent Event to write to clients.
type sseEvent struct {
	Event string `json:"event"` // "started", "record", "completed", "failed"
	Data  string `json:"data"`  // JSON payload
}

// activeRun tracks an in-progress subscription run and its SSE broadcast channel.
type activeRun struct {
	events   chan sseEvent // buffered channel of events for SSE clients
	done     chan struct{} // closed when the run is finished
	registry *RunRegistry  // back-reference for self-cleanup
	subID    string
}

// publish enqueues an SSE event without blocking the producer when no
// consumer is reading. Events are dropped on a full buffer.
func (a *activeRun) publish(evt sseEvent) {
	select {
	case a.events <- evt:
	default:
	}
}

// finish closes the done channel, waits a short grace period so any attached
// SSE client can drain remaining events, then unregisters and closes events.
func (a *activeRun) finish() {
	close(a.done)
	time.Sleep(5 * time.Second)

	if a.registry != nil {
		a.registry.Delete(a.subID)
	}

	close(a.events)
}

// RunRegistry manages active subscription runs (scheduled and on-demand),
// keyed by subscription ID string.
type RunRegistry struct {
	mu   sync.RWMutex
	runs map[string]*activeRun
}

// NewRunRegistry creates an empty registry.
func NewRunRegistry() *RunRegistry {
	return &RunRegistry{
		runs: make(map[string]*activeRun),
	}
}

// TryReserve atomically claims a slot for a subscription run. Returns the
// activeRun and true on success, or (nil, false) if a run is already in
// progress for the subscription.
func (r *RunRegistry) TryReserve(subscriptionID string) (*activeRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.runs[subscriptionID]; exists {
		return nil, false
	}

	run := &activeRun{
		events:   make(chan sseEvent, 1000),
		done:     make(chan struct{}),
		registry: r,
		subID:    subscriptionID,
	}
	r.runs[subscriptionID] = run

	return run, true
}

// Load retrieves an active run by subscription ID.
func (r *RunRegistry) Load(subscriptionID string) (*activeRun, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	run, ok := r.runs[subscriptionID]

	return run, ok
}

// IsActive reports whether a run is currently in progress for the subscription.
func (r *RunRegistry) IsActive(subscriptionID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.runs[subscriptionID]

	return ok
}

// Delete removes an active run entry.
func (r *RunRegistry) Delete(subscriptionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.runs, subscriptionID)
}
