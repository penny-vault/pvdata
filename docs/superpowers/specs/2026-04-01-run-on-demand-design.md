# Run on Demand with Live Progress

## Summary

Add the ability to trigger a subscription run from the web UI and display live progress including a streaming record feed and ticking record count, mirroring the TUI experience.

## Backend

### New API Endpoints

**`POST /api/v1/subscriptions/:id/run`**

Triggers an on-demand run for the subscription. Returns immediately.

- Loads the subscription by ID.
- Rejects with 409 Conflict if a run is already in progress for that subscription.
- Creates a `RunManager` for the single subscription and launches `RunAll` in a goroutine.
- Registers the active run in an in-memory run registry so the SSE endpoint can find it.
- Response: `{"status": "started"}`

**`GET /api/v1/subscriptions/:id/run/events`**

SSE (Server-Sent Events) endpoint that streams run progress to the browser.

- Sets `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`.
- Looks up the active run in the registry. Returns 404 if no run is in progress.
- Streams events until the run completes or the client disconnects:

```
event: started
data: {"subscription_id":"...","name":"..."}

event: record
data: {"count":1,"type":"index_snapshot","summary":"IVV AAPL 0.0712 2026-04-01"}

event: record
data: {"count":2,"type":"index_change","summary":"IVV MSFT add 0.0651 2026-04-01"}

event: completed
data: {"count":1247,"status":"success"}

event: failed
data: {"count":42,"error":"HTTP 429 rate limited"}
```

The `summary` field is a human-readable single-line representation of the record, not the full data structure. This keeps SSE payloads small and the feed readable.

### Run Registry

A `RunRegistry` struct in the `web` package manages active runs:

```go
type activeRun struct {
    manager    *tui.RunManager
    broadcast  chan tui.RunEvent       // fan-out from RunManager.EventChan
    records    chan recordEvent         // observation summaries for SSE
    done       chan struct{}
}

type recordEvent struct {
    Count   int    `json:"count"`
    Type    string `json:"type"`
    Summary string `json:"summary"`
}
```

- `sync.Map` keyed by subscription ID (UUID string).
- The POST handler creates the entry; a cleanup goroutine removes it after the run completes (with a short grace period so late-connecting SSE clients can see the final event).
- The registry intercepts observations through a fan-out channel (same pattern as `RunManager.countObservations`) to produce `recordEvent` summaries.

### Observation Summary Format

Each observation type gets a one-line summary:

| Type | Format |
|------|--------|
| IndexSnapshot | `"{IndexName} {Ticker} weight={Weight} {SnapshotDate}"` |
| IndexChange | `"{IndexName} {Ticker} {Action} weight={Weight} {EventDate}"` |
| EodQuote | `"{Ticker} close={Close} vol={Volume} {Date}"` |
| Fundamental | `"{Ticker} {Dimension} {CalendarDate}"` |
| Other types | `"{type} observation"` (fallback) |

Only the most common types need rich formatting initially. The fallback covers the rest.

## Frontend

### API Layer (`web/ui/src/lib/api.ts`)

```typescript
export async function runSubscription(id: string): Promise<void> {
  const res = await authFetch(`/subscriptions/${id}/run`, { method: 'POST' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export function subscribeRunEvents(id: string): EventSource {
  return new EventSource(`${BASE}/subscriptions/${id}/run/events`)
}
```

Note: `EventSource` does not support custom headers. Since auth is disabled in dev and the SSE endpoint is read-only, this is acceptable. If auth is enabled later, switch to `fetch()` with `ReadableStream` or a polyfill that supports headers.

### Run Now Button (`SubscriptionDetailPage.vue`)

Add a "Run Now" button in the action bar, next to Activate/Deactivate:

```html
<Button
  v-if="subscription.provider !== 'legacy'"
  label="Run Now"
  icon="pi pi-bolt"
  :disabled="runInProgress"
  @click="triggerRun"
/>
```

Hidden for legacy provider (not fetchable).

### Live Progress Panel

A collapsible panel that appears above the tabs when a run is active:

```
+----------------------------------------------------------+
| Running...  Records: 1,247                               |
|                                                          |
| index_snapshot  sp-500 AAPL weight=0.0712 2026-04-01     |
| index_snapshot  sp-500 MSFT weight=0.0651 2026-04-01     |
| index_change    sp-500 NVDA add weight=0.0523 2026-04-01 |
| index_snapshot  sp-500 AMZN weight=0.0401 2026-04-01     |
| ...                                                      |
+----------------------------------------------------------+
```

Behavior:
- Header row shows status (Running/Completed/Failed) and a live record count.
- Body is a scrollable log of record summaries, auto-scrolling to bottom.
- Capped at last 200 entries in the buffer to avoid memory bloat.
- On completion: header updates to "Completed -- 1,247 records" (success, green) or "Failed -- 42 records" (error, red). Panel stays visible until dismissed.
- On completion: auto-refresh run history and subscription stats.

### State Management

All run state lives in the component (no store needed):

```typescript
const runInProgress = ref(false)
const runStatus = ref<'idle' | 'running' | 'completed' | 'failed'>('idle')
const runRecordCount = ref(0)
const runRecords = ref<{ type: string; summary: string }[]>([])
const maxRunRecords = 200
let eventSource: EventSource | null = null
```

The `triggerRun` function POSTs to start the run, then opens an EventSource. Event handlers update the refs. The EventSource is closed on component unmount or when the run completes.

## Files to Create/Modify

| File | Action |
|------|--------|
| `web/run_registry.go` | New -- RunRegistry, activeRun, recordEvent, observation summarizer |
| `web/handlers_run_now.go` | New -- POST handler (TriggerRun) and GET handler (RunEvents SSE) |
| `web/route.go` | Modify -- add two new routes |
| `web/fiber.go` | Modify -- create RunRegistry and inject into app |
| `web/ui/src/lib/api.ts` | Modify -- add runSubscription and subscribeRunEvents |
| `web/ui/src/pages/SubscriptionDetailPage.vue` | Modify -- add Run Now button and live progress panel |

## Not in Scope

- Multiple concurrent runs for different subscriptions (support one active run per subscription, multiple subscriptions can run concurrently).
- Persisting run progress across server restarts (in-memory only).
- Auth on the SSE endpoint (EventSource limitation; address when auth is required).
- Cancel/abort a running subscription from the UI (future enhancement).
