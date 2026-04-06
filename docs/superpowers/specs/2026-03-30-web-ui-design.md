# Web UI Design Spec

## Overview

A web-based dashboard for managing pv-data subscriptions, monitoring import status, browsing imported data, and running ad-hoc SQL queries. Single-user, internet-facing, authenticated via Zitadel OAuth2/OIDC.

## Tech Stack

### Frontend
- **Vue 3** + **Vite** (build tool)
- **@carbon/vue** -- Carbon Design System components (dark theme)
- **@carbon/charts-vue** -- charts and sparklines (D3-based)
- **Tailwind CSS** -- layout utilities alongside Carbon's design tokens
- **Pinia** -- state management
- **Vue Router** -- client-side routing (history mode)
- **CodeMirror** -- SQL editor with syntax highlighting

### Backend
- **Go Fiber v2** -- existing server in `web/`, extended with new endpoints
- **Zitadel OAuth2/OIDC** -- authentication middleware on all API routes
- **parquet-go** (`github.com/parquet-go/parquet-go`) -- Parquet export serialization
- **go:embed** -- Vite build output embedded into Go binary for single-binary deployment

### Database
- PostgreSQL (existing) -- new `run_history` table for persisting run statistics

## Pages & Navigation

### Top-level Navigation (Carbon UI Shell header)
- **Subscriptions** -- default/home, the main dashboard
- **SQL Console** -- global query workbench
- **Settings** -- reserved nav slot for future config (not in v1 scope)

### Subscriptions Page (`/`)

A data table showing all subscriptions with the following columns:

| Column | Source |
|--------|--------|
| Name | `subscriptions.name` |
| Provider | `subscriptions.provider` |
| Dataset | `subscriptions.dataset` |
| Status | Active/inactive flag + last run pass/fail from `run_history` |
| Sparkline | Records imported per run over last 30 days from `run_history` |
| Last Import Time | `subscriptions.last_run` |
| Records Last Import | `subscriptions.num_records_last_import` |
| Next Import Time | Computed from cron schedule via `gocron` |

- Click a row to navigate to the subscription detail page
- "New Subscription" button in the top-right corner

### Subscription Detail Page (`/subscriptions/:id`)

**Header:**
- Subscription name, provider/dataset badges
- Active toggle, edit button, delete button

**Run History Section:**
- Table: date, status (success/fail), records imported, duration
- Chart: records imported over time (line or bar)

**Data Browser Section:**
- Tabs per data type (one tab for each type in the subscription)
- Infinite-scroll table with server-side filter, sort, and search
- Shortcut button to open SQL console pre-scoped to this subscription's table

**Edit Panel:**
- Cron schedule editor
- Provider-specific config fields
- Healthcheck ID

### New Subscription Page (`/subscriptions/new`)

Stepped wizard mirroring the CLI flow:
1. Select provider
2. Select dataset
3. Set cron schedule
4. Fill provider-specific config fields
5. Optionally create healthcheck.io monitor
6. Review and confirm

### SQL Console Page (`/sql`)

- CodeMirror editor with SQL syntax highlighting
- Execute button
- Results displayed in a data table below
- Export buttons: CSV and Parquet
- Query history stored in browser localStorage

## API Endpoints

All endpoints under `/api/v1`, behind Zitadel OAuth middleware.

### Subscriptions
- `GET /api/v1/subscriptions` -- list all subscriptions (extended with sparkline data)
- `POST /api/v1/subscriptions` -- create new subscription
- `GET /api/v1/subscriptions/:id` -- subscription detail with run stats
- `PUT /api/v1/subscriptions/:id` -- update config, schedule, active status
- `DELETE /api/v1/subscriptions/:id` -- delete subscription and associated tables
- `POST /api/v1/subscriptions/:id/activate` -- activate subscription
- `POST /api/v1/subscriptions/:id/deactivate` -- deactivate subscription

### Providers
- `GET /api/v1/providers` -- list all providers with datasets and config descriptions (for creation wizard)

### Run History
- `GET /api/v1/subscriptions/:id/runs` -- paginated run history
- `GET /api/v1/subscriptions/:id/runs/sparkline` -- last 30 days aggregated for sparkline rendering

### Data Browser
- `GET /api/v1/subscriptions/:id/data/:datatype` -- server-side filter/sort/page via query params (`?q=&sort=&order=&cursor=&limit=`)

### SQL Console
- `POST /api/v1/sql` -- execute query, return JSON results
- `POST /api/v1/sql/export?format=csv` -- execute query, return CSV file
- `POST /api/v1/sql/export?format=parquet` -- execute query, return Parquet file

### Static Assets
- Fiber serves the embedded Vite build output for all non-API routes
- SPA fallback route for Vue Router history mode

## Database Changes

### New Table: `run_history`

```sql
CREATE TABLE run_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    start_time TIMESTAMP NOT NULL,
    end_time TIMESTAMP NOT NULL,
    num_observations INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('success', 'failed')),
    created_on TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_run_history_subscription_id ON run_history(subscription_id);
CREATE INDEX idx_run_history_start_time ON run_history(start_time);
```

New migration file in `db/migrations/`.

### Code Changes for Run History Persistence

After each run completes, persist the `RunSummary` to `run_history`:
- `cmd/run.go` -- in `runSubscription()`, after the summary is received from `exitChan`
- `tui/runmanager.go` -- in `RunAll()`, after receiving from `exitChan`

## Authentication

### Zitadel OAuth2/OIDC

- Fiber middleware validates Bearer token on all `/api/v1/*` routes
- Frontend redirects to Zitadel login if no valid session
- Authorization code flow: Zitadel redirects back, code exchanged for tokens
- Access token in browser memory, refresh token in httpOnly cookie
- Config values in `.pvdata.toml`: `auth.domain`, `auth.client_id`, `auth.client_secret`, `auth.redirect_uri`

### SQL Endpoint Safety

- All queries wrapped in a read-only transaction (`SET TRANSACTION READ ONLY`)
- 30-second query timeout to prevent runaway queries
- No additional authorization beyond the OAuth gate (single user)

## Development Workflow

- **Dev:** Vite dev server on port 5173, proxies `/api` to Fiber backend
- **Prod:** `npm run build` in `web/ui/`, output embedded via `go:embed`, single binary serves everything

## Design Aesthetic

Dark theme following Carbon Design System (g100 theme). Reference: IBM Watson Studio dashboard aesthetic.

- Near-black backgrounds
- Blue/purple accent palette
- Card-based layouts with subtle borders
- Large stat callouts for key metrics
- Clean typography (IBM Plex)
