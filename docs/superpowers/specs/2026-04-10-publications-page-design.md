# Publications Page Design

## Overview

Add a web UI page for managing published views -- the system that combines multiple subscription data tables into unified database views (e.g. a single `eod` view backed by multiple provider tables with date-range boundaries).

The web UI mirrors the existing TUI flow in `tui/publish.go`: list views, create by picking an available data type, then manage sources on a detail page.

## Backend API

New handler file: `web/handlers_publications.go`

New routes registered in `web/route.go` under `/api/v1/publications`:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/publications` | List all published views |
| POST | `/publications` | Create an empty published view |
| GET | `/publications/:id` | Get a single published view with enriched source info |
| PUT | `/publications/:id` | Update sources array |
| DELETE | `/publications/:id` | Drop DB view and delete row |
| GET | `/publications/:id/candidates` | Get candidate source tables for adding |
| GET | `/publications/available-types` | Get data types eligible for new views |

### Create (POST `/publications`)

Request body:

```json
{
  "data_type_key": "eod"
}
```

The server derives `view_name` from `data.DataTypes[data_type_key].ViewName`. The view is created with an empty sources array (which generates a DROP VIEW, matching TUI behavior). Returns the created `PublishedView` with its ID.

### Get (GET `/publications/:id`)

Returns the published view with each source enriched with subscription metadata:

```json
{
  "id": "uuid",
  "view_name": "eod",
  "data_type_key": "eod",
  "sources": [
    {
      "table_name": "tiingo_eod_eod_abc123",
      "subscription_id": "uuid",
      "subscription_name": "Tiingo EOD",
      "provider": "tiingo",
      "dataset": "eod",
      "from_date": "2020-01-01",
      "until_date": null
    }
  ]
}
```

The enrichment (subscription_name, provider, dataset) is computed server-side by looking up each source's subscription_id against the subscriptions table, so the frontend doesn't need extra round-trips.

### Candidates (GET `/publications/:id/candidates`)

Returns subscription tables that:
1. Belong to subscriptions whose data types include the view's `data_type_key`
2. Are not already in the view's sources

Response:

```json
[
  {
    "table_name": "polygon_eod_eod_def456",
    "subscription_id": "uuid",
    "subscription_name": "Polygon Stocks",
    "provider": "polygon",
    "dataset": "stocks"
  }
]
```

### Update (PUT `/publications/:id`)

Request body is the full sources array:

```json
{
  "sources": [
    {
      "table_name": "tiingo_eod_eod_abc123",
      "subscription_id": "uuid",
      "from_date": "2020-01-01",
      "until_date": null
    }
  ]
}
```

The server calls `SavePublishedView` which validates source tables exist and applies the updated view SQL.

### Delete (DELETE `/publications/:id`)

Drops the database view and deletes the `published_views` row. Returns 204.

### Overlap validation change

`ValidateSources()` in `library/published_views.go` is changed from returning an error to returning overlap information. `SavePublishedView` no longer rejects overlapping date ranges. The API returns overlap warnings in the response so the UI can display them, but submission is not blocked.

## Frontend

### New files

- `web/ui/src/pages/PublicationsPage.vue` -- list page
- `web/ui/src/pages/PublicationDetailPage.vue` -- detail/edit/create page
- `web/ui/src/pages/NewPublicationPage.vue` -- data type picker for creation

### API module additions (`web/ui/src/lib/api.ts`)

```typescript
getPublications()                           // GET /publications
createPublication(body)                     // POST /publications
getPublication(id)                          // GET /publications/:id
updatePublication(id, body)                 // PUT /publications/:id
deletePublication(id)                       // DELETE /publications/:id
getPublicationCandidates(id)                // GET /publications/:id/candidates
getAvailablePublicationTypes()              // GET /publications/available-types
```

### Routes (`web/ui/src/router/index.ts`)

- `/publications` -- PublicationsPage
- `/publications/new` -- NewPublicationPage
- `/publications/:id` -- PublicationDetailPage

### Navigation (`web/ui/src/App.vue`)

Add "Publications" as a fourth item in the header menubar, between "SQL Console" and "Data Quality".

### List Page (`/publications`)

PrimeVue DataTable with columns:

| Column | Content |
|--------|---------|
| View Name | Clickable, navigates to `/publications/:id` |
| Data Type | The data type key |
| Sources | Source count |

"New" button in the top-right navigates to `/publications/new`.

Standard loading/error pattern matching existing pages.

### New Publication Page (`/publications/new`)

On mount, fetches `GET /publications/available-types` which returns data types where `DataType.ViewName` is set and no published view exists yet:

```json
[
  {"data_type_key": "metric", "view_name": "metric"},
  {"data_type_key": "fundamental", "view_name": "fundamental"}
]
```

Single-column PrimeVue DataTable showing these available types. Selecting one POSTs to create the empty view and redirects to `/publications/:id`.

### Detail Page (`/publications/:id`)

Single-panel layout. View name as heading.

**Sources table** (PrimeVue DataTable):

| Column | Content |
|--------|---------|
| Source Table | Database table name |
| Subscription | Subscription name |
| Provider/Dataset | Combined string |
| From | YYYY-MM-DD or empty |
| Until | YYYY-MM-DD or empty |
| Actions | Edit dates / Remove icon buttons |

**Add source button** above the table. Opens a PrimeVue Dialog with a DataTable of candidates (table name, subscription name, provider/dataset). Selecting one PUTs the updated sources array (appending the new source) and refreshes.

**Edit dates dialog**: Two date input fields (from/until). Empty means unbounded. Client-side date format validation. If overlapping date ranges are detected after the edit, a PrimeVue Message with severity "warn" is shown in the dialog. Submission is not blocked. Saving PUTs the updated sources array.

**Remove source dialog**: Confirmation dialog. If removing the last source, warns that the entire view will be deleted. On confirm, either PUTs the sources array without that source, or DELETEs the entire view and redirects to the list page.

**Delete view button**: Danger-styled button at the bottom. Confirmation dialog (matching the pattern used for subscription deletion). DELETEs and redirects to list.

## Overlap Warning Logic

Client-side overlap detection mirrors `ValidateSources()`: sort sources by from_date, check if any source's from_date is before the previous source's until_date. Display as a PrimeVue Message with severity "warn" in the edit dates dialog and on the detail page when overlaps exist in the current sources.

## Testing

Unit tests for the new backend handlers using synthetic data (no real database), following existing test patterns. Frontend testing follows existing conventions.
