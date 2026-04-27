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
	"bytes"
	"strings"
	"sync"
)

// LogCapture is an io.Writer that intercepts zerolog's JSON output, picks
// out events tagged with a SubscriptionID matching an active run, and:
//
//  1. publishes a "log" SSE event to the run's event channel so connected
//     clients see logs streaming in real time,
//  2. appends the line to a per-run buffer that the run pipeline drains and
//     persists alongside the run history record.
//
// The writer also forwards every line untouched to a passthrough writer
// (typically the existing zerolog ConsoleWriter) so dev/console output is
// unchanged.
type LogCapture struct {
	registry   *RunRegistry
	mu         sync.Mutex
	buffers    map[string]*bytes.Buffer
	maxBufSize int
}

// NewLogCapture creates a capture sink bound to the given registry.
// maxBufSize caps each per-run buffer (in bytes) to keep memory bounded
// for very long runs; 0 means use a 4 MiB default.
func NewLogCapture(registry *RunRegistry, maxBufSize int) *LogCapture {
	if maxBufSize <= 0 {
		maxBufSize = 4 * 1024 * 1024
	}

	return &LogCapture{
		registry:   registry,
		buffers:    make(map[string]*bytes.Buffer),
		maxBufSize: maxBufSize,
	}
}

// Write implements io.Writer. Each Write call is one zerolog event in JSON.
func (lc *LogCapture) Write(p []byte) (int, error) {
	subID := extractSubscriptionID(p)
	if subID == "" {
		return len(p), nil
	}

	run, ok := lc.registry.Load(subID)
	if !ok {
		return len(p), nil
	}

	// Stream to connected SSE clients (non-blocking).
	run.publish(sseEvent{Event: "log", Data: string(bytes.TrimRight(p, "\n"))})

	// Append to the per-run capture buffer for DB persistence.
	lc.mu.Lock()
	defer lc.mu.Unlock()

	buf, exists := lc.buffers[subID]
	if !exists {
		buf = &bytes.Buffer{}
		lc.buffers[subID] = buf
	}

	if buf.Len()+len(p) <= lc.maxBufSize {
		buf.Write(p)
	}

	return len(p), nil
}

// Drain returns the captured log buffer for the subscription and clears it.
// Call once when a run completes.
func (lc *LogCapture) Drain(subID string) string {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	buf, ok := lc.buffers[subID]
	if !ok {
		return ""
	}

	s := buf.String()

	delete(lc.buffers, subID)

	return s
}

// extractSubscriptionID parses one zerolog JSON line for a SubscriptionID
// or subscription_id field. Returns "" when no UUID-shaped value is found.
//
// We use a string scan rather than json.Unmarshal because Write is on the
// hot path and most lines do not pertain to an active run.
func extractSubscriptionID(p []byte) string {
	const (
		key1 = `"SubscriptionID":"`
		key2 = `"subscription_id":"`
	)

	s := string(p)

	if idx := strings.Index(s, key1); idx >= 0 {
		return extractQuotedValue(s[idx+len(key1):])
	}

	if idx := strings.Index(s, key2); idx >= 0 {
		return extractQuotedValue(s[idx+len(key2):])
	}

	return ""
}

func extractQuotedValue(s string) string {
	end := strings.IndexByte(s, '"')
	if end < 0 {
		return ""
	}

	return s[:end]
}
