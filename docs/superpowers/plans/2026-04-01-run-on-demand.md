# Run on Demand Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow users to trigger a subscription run from the web UI with live streaming progress (record feed + count).

**Architecture:** A `RunRegistry` manages active runs in-memory. `POST /run` starts a fetch goroutine (same pipeline as `RunManager.RunAll`) with an observation interceptor that summarizes each record into SSE events. `GET /run/events` streams those events to the browser. The frontend uses `EventSource` with query-param auth, displaying a live progress panel with scrolling record feed.

**Tech Stack:** Go/Fiber (SSE via `SetBodyStreamWriter`), Vue 3 + PrimeVue, native `EventSource` with query param auth.

**Spec:** `docs/superpowers/specs/2026-04-01-run-on-demand-design.md`

---

### Task 1: Observation Summarizer

**Files:**
- Create: `web/observation_summary.go`
- Create: `web/observation_summary_test.go`

- [ ] **Step 1: Write tests for observation summarizer**

Create `web/observation_summary_test.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
package web

import (
	"testing"
	"time"

	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestObservationSummary(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Observation Summary Suite")
}

var _ = Describe("summarizeObservation", func() {
	It("summarizes an IndexSnapshot", func() {
		obs := &data.Observation{
			IndexSnapshot: &data.IndexSnapshot{
				IndexName:    "sp-500",
				Ticker:       "AAPL",
				Weight:       0.0712,
				SnapshotDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("index_snapshot"))
		Expect(summary).To(Equal("sp-500 AAPL weight=0.0712 2026-04-01"))
	})

	It("summarizes an IndexChange", func() {
		obs := &data.Observation{
			IndexChange: &data.IndexChange{
				IndexName: "sp-500",
				Ticker:    "NVDA",
				Action:    "add",
				Weight:    0.0523,
				EventDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("index_change"))
		Expect(summary).To(Equal("sp-500 NVDA add weight=0.0523 2026-04-01"))
	})

	It("summarizes an EodQuote", func() {
		obs := &data.Observation{
			EodQuote: &data.Eod{
				Ticker: "MSFT",
				Close:  425.50,
				Volume: 32000000,
				Date:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("eod"))
		Expect(summary).To(Equal("MSFT close=425.50 vol=32000000 2026-04-01"))
	})

	It("summarizes a Fundamental", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:       "AAPL",
				Dimension:    "ARQ",
				CalendarDate: time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("fundamental"))
		Expect(summary).To(Equal("AAPL ARQ 2026-03-31"))
	})

	It("falls back for unknown types", func() {
		obs := &data.Observation{
			MarketHoliday: &data.MarketHoliday{},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("observation"))
		Expect(summary).To(Equal("observation"))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race ./web/ --focus "summarizeObservation"`
Expected: compilation error, `summarizeObservation` not defined.

- [ ] **Step 3: Implement the summarizer**

Create `web/observation_summary.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
package web

import (
	"fmt"

	"github.com/penny-vault/pvdata/data"
)

// summarizeObservation returns a type tag and a human-readable one-line summary.
func summarizeObservation(obs *data.Observation) (string, string) {
	switch {
	case obs.IndexSnapshot != nil:
		s := obs.IndexSnapshot
		return "index_snapshot", fmt.Sprintf("%s %s weight=%.4f %s",
			s.IndexName, s.Ticker, s.Weight, s.SnapshotDate.Format("2006-01-02"))

	case obs.IndexChange != nil:
		c := obs.IndexChange
		return "index_change", fmt.Sprintf("%s %s %s weight=%.4f %s",
			c.IndexName, c.Ticker, c.Action, c.Weight, c.EventDate.Format("2006-01-02"))

	case obs.EodQuote != nil:
		e := obs.EodQuote
		return "eod", fmt.Sprintf("%s close=%.2f vol=%.0f %s",
			e.Ticker, e.Close, e.Volume, e.Date.Format("2006-01-02"))

	case obs.Fundamental != nil:
		f := obs.Fundamental
		return "fundamental", fmt.Sprintf("%s %s %s",
			f.Ticker, f.Dimension, f.CalendarDate.Format("2006-01-02"))

	default:
		return "observation", "observation"
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race ./web/ --focus "summarizeObservation"`
Expected: all 5 specs pass.

- [ ] **Step 5: Commit**

```bash
git add web/observation_summary.go web/observation_summary_test.go
git commit -m "feat(web): add observation summarizer for SSE record feed"
```

---

### Task 2: Run Registry

**Files:**
- Create: `web/run_registry.go`
- Create: `web/run_registry_test.go`

- [ ] **Step 1: Write tests for RunRegistry**

Create `web/run_registry_test.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
package web

import (
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RunRegistry", func() {
	var registry *RunRegistry

	BeforeEach(func() {
		registry = NewRunRegistry()
	})

	It("registers and retrieves an active run", func() {
		id := uuid.New().String()
		run := &activeRun{
			events: make(chan sseEvent, 10),
			done:   make(chan struct{}),
		}
		registry.Store(id, run)

		got, ok := registry.Load(id)
		Expect(ok).To(BeTrue())
		Expect(got).To(Equal(run))
	})

	It("returns false for unknown subscription", func() {
		_, ok := registry.Load("nonexistent")
		Expect(ok).To(BeFalse())
	})

	It("removes a run", func() {
		id := uuid.New().String()
		run := &activeRun{
			events: make(chan sseEvent, 10),
			done:   make(chan struct{}),
		}
		registry.Store(id, run)
		registry.Delete(id)

		_, ok := registry.Load(id)
		Expect(ok).To(BeFalse())
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race ./web/ --focus "RunRegistry"`
Expected: compilation error, types not defined.

- [ ] **Step 3: Implement RunRegistry**

Create `web/run_registry.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
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
	mu    sync.RWMutex
	runs  map[string]*activeRun
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race ./web/ --focus "RunRegistry"`
Expected: all 3 specs pass.

- [ ] **Step 5: Commit**

```bash
git add web/run_registry.go web/run_registry_test.go
git commit -m "feat(web): add RunRegistry for managing active on-demand runs"
```

---

### Task 3: Run Trigger Handler (POST)

**Files:**
- Create: `web/handlers_run_now.go`
- Modify: `web/route.go`
- Modify: `web/fiber.go`

- [ ] **Step 1: Create the handler file with TriggerRun**

Create `web/handlers_run_now.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
)

// getRegistry retrieves the RunRegistry from request context.
func getRegistry(c *fiber.Ctx) *RunRegistry {
	return c.Locals("registry").(*RunRegistry)
}

// TriggerRun starts an on-demand run for a subscription.
// Accepts optional query param: ?lookback=30d (e.g. "7d", "30d", "365d"). Default: 14d.
func TriggerRun(c *fiber.Ctx) error {
	id := c.Params("id")
	myLibrary := getLibrary(c)
	registry := getRegistry(c)
	ctx := c.UserContext()

	sub, err := myLibrary.SubscriptionFromID(ctx, id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	subID := sub.ID.String()

	// Reject if already running
	if _, ok := registry.Load(subID); ok {
		return c.Status(fiber.StatusConflict).JSON(HttpError{
			Code:    "409",
			Message: "a run is already in progress for this subscription",
		})
	}

	// Parse lookback duration from query param (e.g. "30d")
	lookback := 14 * 24 * time.Hour // default 14 days
	if lb := c.Query("lookback"); lb != "" {
		parsed, err := parseLookback(lb)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(HttpError{
				Code:    "400",
				Message: "invalid lookback: " + err.Error(),
			})
		}
		lookback = parsed
	}

	// Validate provider and dataset exist
	subProvider, ok := provider.Map[sub.Provider]
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "provider not found: " + sub.Provider,
		})
	}

	if _, ok := subProvider.Datasets()[sub.Dataset]; !ok {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "dataset not found: " + sub.Dataset,
		})
	}

	// Create registry entry with buffered channels
	run := &activeRun{
		events: make(chan sseEvent, 1000),
		done:   make(chan struct{}),
	}
	registry.Store(subID, run)

	// Launch the run in a background goroutine
	go executeRun(myLibrary, sub, run, registry, lookback)

	return c.JSON(fiber.Map{"status": "started"})
}

// parseLookback parses a duration string like "14d", "30d", "365d" into time.Duration.
func parseLookback(s string) (time.Duration, error) {
	if len(s) < 2 || s[len(s)-1] != 'd' {
		return 0, fmt.Errorf("expected format like '14d', got %q", s)
	}
	days := 0
	for _, c := range s[:len(s)-1] {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("expected format like '14d', got %q", s)
		}
		days = days*10 + int(c-'0')
	}
	if days == 0 {
		return 0, fmt.Errorf("lookback must be at least 1d")
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// executeRun runs the subscription and feeds events into the activeRun channels.
func executeRun(myLibrary *library.Library, sub *library.Subscription, run *activeRun, registry *RunRegistry, lookback time.Duration) {
	subID := sub.ID.String()

	defer func() {
		close(run.done)
		// Grace period so SSE clients can read the final event
		time.Sleep(5 * time.Second)
		registry.Delete(subID)
		close(run.events)
	}()

	ctx := context.Background()

	// Manage partitions and migrations
	if err := sub.ManagePartitions(ctx); err != nil {
		log.Error().Err(err).Msg("ManagePartitions failed during on-demand run")
	}

	if err := sub.RunMigrations(ctx); err != nil {
		log.Error().Err(err).Msg("RunMigrations failed during on-demand run")
	}

	subProvider := provider.Map[sub.Provider]
	subDataset := subProvider.Datasets()[sub.Dataset]

	// Channels for data flow
	observeChan := make(chan *data.Observation, 1000)
	saveChan := make(chan *data.Observation, 1000)
	exitChan := make(chan data.RunSummary, 1)

	var wg sync.WaitGroup
	wg.Add(1)

	go myLibrary.SaveObservations(saveChan, &wg)

	// Send started event
	startedData, _ := json.Marshal(map[string]string{
		"subscription_id": sub.ID.String(),
		"name":            sub.Name,
	})
	run.events <- sseEvent{Event: "started", Data: string(startedData)}

	// Observation interceptor: summarize each record and forward to saveChan
	go func() {
		count := 0
		for obs := range observeChan {
			count++
			typ, summary := summarizeObservation(obs)
			d, _ := json.Marshal(map[string]interface{}{
				"count":   count,
				"type":    typ,
				"summary": summary,
			})
			run.events <- sseEvent{Event: "record", Data: string(d)}
			saveChan <- obs
		}
		close(saveChan)
	}()

	// Run the fetch with lookback injected into context
	fetchCtx := context.WithValue(ctx, provider.LookbackKey, lookback)
	fetchLogger := log.With().Str("SubscriptionID", sub.ID.String()).Logger()
	fetchCtx = fetchLogger.WithContext(fetchCtx)

	subDataset.Fetch(fetchCtx, sub, observeChan, exitChan)

	// Wait for fetch to complete
	summary := <-exitChan
	close(observeChan)

	// Persist run history
	if err := myLibrary.SaveRunHistory(ctx, summary); err != nil {
		log.Error().Err(err).Str("Subscription", sub.Name).Msg("failed to save run history")
	}

	// Run post-fetch hooks
	if summary.Status == data.RunSuccess && len(subDataset.PostFetch) > 0 {
		for _, hook := range subDataset.PostFetch {
			if err := hook(ctx, sub); err != nil {
				log.Error().Err(err).Str("Subscription", sub.Name).Msg("post-fetch hook failed")
				break
			}
		}
	}

	// Emit final event
	if summary.Status == data.RunFailed {
		d, _ := json.Marshal(map[string]interface{}{
			"count": summary.NumObservations,
			"error": "run failed",
		})
		run.events <- sseEvent{Event: "failed", Data: string(d)}
	} else {
		d, _ := json.Marshal(map[string]interface{}{
			"count":  summary.NumObservations,
			"status": "success",
		})
		run.events <- sseEvent{Event: "completed", Data: string(d)}
	}

	wg.Wait()
}
```

Note: This bypasses `RunManager.RunAll()` and runs the fetch directly (same logic, but with our own observation channel so we can intercept every record). The `RunManager` event fan-out goroutine is not used; we emit lifecycle events ourselves.

- [ ] **Step 2: Add the SSE handler to the same file**

Append to `web/handlers_run_now.go`:

```go
// RunEvents streams Server-Sent Events for an active subscription run.
func RunEvents(c *fiber.Ctx) error {
	id := c.Params("id")
	myLibrary := getLibrary(c)
	registry := getRegistry(c)

	// Resolve full subscription ID from prefix
	sub, err := myLibrary.SubscriptionFromID(c.UserContext(), id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	subID := sub.ID.String()

	run, ok := registry.Load(subID)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "no active run for this subscription",
		})
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		for {
			select {
			case evt, ok := <-run.events:
				if !ok {
					// Channel closed, run is done
					return
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data)
				if err := w.Flush(); err != nil {
					return
				}
			case <-run.done:
				// Drain any remaining events
				for {
					select {
					case evt, ok := <-run.events:
						if !ok {
							return
						}
						fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data)
						w.Flush()
					default:
						return
					}
				}
			}
		}
	})

	return nil
}
```

Add `bufio` to the import block at the top of the file:

```go
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
)
```

- [ ] **Step 3: Wire up the registry in fiber.go**

In `web/fiber.go`, add the registry to CreateFiberApp. Change:

```go
func CreateFiberApp(myLibrary *library.Library) *fiber.App {
```

To:

```go
func CreateFiberApp(myLibrary *library.Library) *fiber.App {
	registry := NewRunRegistry()
```

And add the registry to the middleware that injects locals, changing:

```go
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("library", myLibrary)
		return c.Next()
	})
```

To:

```go
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("library", myLibrary)
		c.Locals("registry", registry)
		return c.Next()
	})
```

- [ ] **Step 4: Add routes in route.go**

In `web/route.go`, add after the `api.Post("/subscriptions/:id/deactivate", ...)` line:

```go
	api.Post("/subscriptions/:id/run", TriggerRun)
	api.Get("/subscriptions/:id/run/events", RunEvents)
```

- [ ] **Step 5: Verify compilation**

Run: `go build ./...`
Expected: compiles with no errors.

- [ ] **Step 6: Commit**

```bash
git add web/handlers_run_now.go web/route.go web/fiber.go
git commit -m "feat(web): add run trigger and SSE event streaming endpoints"
```

---

### Task 4: Auth Query Param Support for SSE

**Files:**
- Modify: `web/auth.go`

- [ ] **Step 1: Update auth middleware to check query param**

In `web/auth.go`, in the authenticated handler (the closure returned when `domain != ""`), change:

```go
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "missing authorization header",
			})
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "invalid authorization header format",
			})
		}

		tokenString := parts[1]
```

To:

```go
		// Check Authorization header first, then fall back to ?token= query param (for SSE)
		var tokenString string

		authHeader := c.Get("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				tokenString = parts[1]
			}
		}

		if tokenString == "" {
			tokenString = c.Query("token")
		}

		if tokenString == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "missing authorization",
			})
		}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./...`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add web/auth.go
git commit -m "feat(web): support JWT auth via query param for SSE endpoints"
```

---

### Task 5: Frontend API Functions

**Files:**
- Modify: `web/ui/src/lib/api.ts`

- [ ] **Step 1: Add runSubscription function**

Append to `web/ui/src/lib/api.ts` before the closing of the file, in a new section:

```typescript
// ---------- Run On Demand ----------

export async function runSubscription(id: string, lookback?: string): Promise<void> {
  const qs = lookback ? `?lookback=${encodeURIComponent(lookback)}` : ''
  const res = await authFetch(`/subscriptions/${id}/run${qs}`, { method: 'POST' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export async function subscribeRunEvents(id: string): Promise<EventSource> {
  const token = await getAccessToken()
  const qs = token ? `?token=${encodeURIComponent(token)}` : ''
  return new EventSource(`${BASE}/subscriptions/${id}/run/events${qs}`)
}
```

- [ ] **Step 2: Verify build**

Run: `cd web/ui && npx vue-tsc --noEmit`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add web/ui/src/lib/api.ts
git commit -m "feat(ui): add runSubscription and subscribeRunEvents API functions"
```

---

### Task 6: Run Now Button and Live Progress Panel

**Files:**
- Modify: `web/ui/src/pages/SubscriptionDetailPage.vue`

- [ ] **Step 1: Add imports and state**

In `SubscriptionDetailPage.vue`, add to the imports:

```typescript
import { runSubscription, subscribeRunEvents } from '@/lib/api'
import ProgressBar from 'primevue/progressbar'
```

Add state variables after the existing refs (after `const deleteConfirmText = ref('')`):

```typescript
const runStatus = ref<'idle' | 'running' | 'completed' | 'failed'>('idle')
const runRecordCount = ref(0)
const runRecords = ref<{ type: string; summary: string }[]>([])
const maxRunRecords = 200
const runLookback = ref('14d')
let eventSource: EventSource | null = null
```

- [ ] **Step 2: Add triggerRun and cleanup functions**

Add after the existing function definitions (after `formatDate` and before `async function loadSubscription`):

```typescript
async function triggerRun() {
  try {
    runStatus.value = 'running'
    runRecordCount.value = 0
    runRecords.value = []
    error.value = ''

    await runSubscription(id.value, runLookback.value)

    eventSource = await subscribeRunEvents(id.value)

    eventSource.addEventListener('started', () => {
      runStatus.value = 'running'
    })

    eventSource.addEventListener('record', (e: MessageEvent) => {
      const data = JSON.parse(e.data)
      runRecordCount.value = data.count
      runRecords.value.push({ type: data.type, summary: data.summary })
      if (runRecords.value.length > maxRunRecords) {
        runRecords.value = runRecords.value.slice(-maxRunRecords)
      }
    })

    eventSource.addEventListener('completed', (e: MessageEvent) => {
      const data = JSON.parse(e.data)
      runRecordCount.value = data.count
      runStatus.value = 'completed'
      eventSource?.close()
      eventSource = null
      loadSubscription()
      loadRuns()
    })

    eventSource.addEventListener('failed', (e: MessageEvent) => {
      const data = JSON.parse(e.data)
      runRecordCount.value = data.count
      runStatus.value = 'failed'
      error.value = data.error || 'Run failed'
      eventSource?.close()
      eventSource = null
      loadSubscription()
      loadRuns()
    })

    eventSource.onerror = () => {
      if (runStatus.value === 'running') {
        runStatus.value = 'failed'
        error.value = 'Lost connection to run event stream'
      }
      eventSource?.close()
      eventSource = null
    }
  } catch (e: any) {
    runStatus.value = 'failed'
    error.value = e.message || 'Failed to start run'
  }
}

function dismissRunPanel() {
  runStatus.value = 'idle'
  runRecords.value = []
  runRecordCount.value = 0
}
```

Add cleanup on unmount. Add `onUnmounted` to the vue import:

```typescript
import { ref, computed, onMounted, onUnmounted } from 'vue'
```

And add after the existing `onMounted`:

```typescript
onUnmounted(() => {
  eventSource?.close()
})
```

- [ ] **Step 3: Add the Run Now button to the template**

In the button group div (the `<div style="display: flex; gap: 0.5rem; flex-wrap: wrap">` that contains Activate/Edit/Delete buttons), add before the Activate button:

```html
          <div v-if="subscription.provider !== 'legacy'" style="display: flex; align-items: center; gap: 0.5rem">
            <InputText v-model="runLookback" placeholder="14d" style="width: 5rem" :disabled="runStatus === 'running'" />
            <Button label="Run Now" icon="pi pi-bolt" severity="info" :disabled="runStatus === 'running'" @click="triggerRun" />
          </div>
```

- [ ] **Step 4: Add the live progress panel to the template**

Add the progress panel after the `<Message>` error display (after `<Message v-if="error" ...>`) and before the stats cards grid (`<div v-if="!editing" style="display: grid; ...>`):

```html
      <div v-if="runStatus !== 'idle'" style="margin-bottom: 1rem; border: 1px solid var(--p-content-border-color); border-radius: 8px; overflow: hidden">
        <div :style="{
          padding: '0.75rem 1rem',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          background: runStatus === 'completed' ? 'var(--p-green-900)' : runStatus === 'failed' ? 'var(--p-red-900)' : 'var(--p-surface-800)',
        }">
          <div style="display: flex; align-items: center; gap: 0.5rem">
            <i v-if="runStatus === 'running'" class="pi pi-spin pi-spinner" />
            <i v-else-if="runStatus === 'completed'" class="pi pi-check-circle" />
            <i v-else class="pi pi-times-circle" />
            <span style="font-weight: 600">
              {{ runStatus === 'running' ? 'Running...' : runStatus === 'completed' ? 'Completed' : 'Failed' }}
            </span>
          </div>
          <div style="display: flex; align-items: center; gap: 1rem">
            <span>Records: {{ runRecordCount.toLocaleString() }}</span>
            <Button v-if="runStatus !== 'running'" icon="pi pi-times" text size="small" @click="dismissRunPanel" />
          </div>
        </div>
        <div v-if="runStatus === 'running'" style="height: 2px">
          <ProgressBar mode="indeterminate" style="height: 2px" />
        </div>
        <div ref="runLogRef" style="max-height: 300px; overflow-y: auto; font-family: monospace; font-size: 12px; padding: 0.5rem; background: var(--p-surface-900)">
          <div v-for="(rec, i) in runRecords" :key="i" style="padding: 1px 0; white-space: nowrap">
            <span style="opacity: 0.5; margin-right: 0.5rem">{{ rec.type }}</span>
            <span>{{ rec.summary }}</span>
          </div>
        </div>
      </div>
```

- [ ] **Step 5: Add auto-scroll behavior**

Add a template ref and watcher for auto-scrolling. Add `watch, nextTick` to the vue import:

```typescript
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
```

Add the template ref with the other refs:

```typescript
const runLogRef = ref<HTMLElement | null>(null)
```

Add a watcher after the `dismissRunPanel` function:

```typescript
watch(runRecords, () => {
  nextTick(() => {
    if (runLogRef.value) {
      runLogRef.value.scrollTop = runLogRef.value.scrollHeight
    }
  })
}, { deep: true })
```

- [ ] **Step 6: Verify build**

Run: `cd web/ui && npx vue-tsc --noEmit && npm run build`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add web/ui/src/pages/SubscriptionDetailPage.vue
git commit -m "feat(ui): add Run Now button with live progress panel and record feed"
```

---

### Task 7: Integration Test

**Files:**
- (no new files -- manual verification)

- [ ] **Step 1: Build and start the server**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data
make build
./pvdata serve
```

- [ ] **Step 2: Verify POST /run endpoint**

Open browser to a subscription detail page. Click "Run Now". Verify:
- Button disables while running
- Progress panel appears with "Running..." header
- Record count ticks up
- Live records scroll in the log area
- On completion, header turns green with "Completed"
- Run history and stats refresh

- [ ] **Step 3: Verify 409 conflict**

While a run is in progress, try clicking "Run Now" again (it should be disabled). Also verify via curl:

```bash
curl -X POST http://localhost:3000/api/v1/subscriptions/<id>/run
# Should return 409 if already running
```

- [ ] **Step 4: Verify SSE endpoint directly**

```bash
curl -N http://localhost:3000/api/v1/subscriptions/<id>/run/events
# Should stream events as they arrive
```

- [ ] **Step 5: Commit the built frontend**

```bash
npm run build --prefix web/ui
git add web/ui/dist/
git commit -m "build: update frontend dist with run-on-demand feature"
```

---

### Task 8: Lint and Final Cleanup

- [ ] **Step 1: Run linter**

```bash
golangci-lint run --fix ./...
```

Fix any issues.

- [ ] **Step 2: Run Go tests**

```bash
ginkgo run -race ./web/
```

Verify all tests pass.

- [ ] **Step 3: Run frontend type check**

```bash
cd web/ui && npx vue-tsc --noEmit
```

- [ ] **Step 4: Final commit if any changes**

```bash
git add -A
git commit -m "chore: lint fixes for run-on-demand feature"
```
