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

import "sync"

// sseEvent represents a single Server-Sent Event to write to clients.
type sseEvent struct {
	Event string `json:"event"` // "started", "record", "completed", "failed"
	Data  string `json:"data"`  // JSON payload
}

// activeRun tracks an in-progress subscription run and its SSE broadcast channel.
type activeRun struct {
	events chan sseEvent // buffered channel of events for SSE clients
	done   chan struct{} // closed when the run is finished
}

// RunRegistry manages active on-demand runs, keyed by subscription ID string.
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

// Store registers an active run for a subscription ID.
func (r *RunRegistry) Store(subscriptionID string, run *activeRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[subscriptionID] = run
}

// Load retrieves an active run by subscription ID.
func (r *RunRegistry) Load(subscriptionID string) (*activeRun, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.runs[subscriptionID]
	return run, ok
}

// Delete removes an active run entry.
func (r *RunRegistry) Delete(subscriptionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, subscriptionID)
}
