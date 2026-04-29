# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.5.0] - 2026-04-28

### Added

- Live in-flight runs in the web UI. The subscriptions list shows the most recent run's status as a chip on every row and refreshes itself; the detail page's run panel renders rows that are currently running with a live progress count and reconnects automatically if the SSE stream drops, with bounded retry/backoff. Run lifecycle is now persisted to `run_history` from start to finish (including a `running` status) so progress survives reload, and abandoned `running` rows from a prior process are reconciled to `failed` when `pvdata serve` boots.
- Asset icons and logos. Asset records now carry `icon_url` and `logo_url`. When a `[filers]` block points at the new Backblaze B2 backend, the Massive provider downloads each asset's branding, uploads to a public bucket, and stores the resulting URL on the asset row. Per-run download budgets keep fetches bounded, and the data browser renders the URLs as inline images. Massive's asset-detail view has a new "missing branding" filter lane to surface tickers without resolved URLs.
- Priority-based dedup for the published assets view. The view now picks the highest-priority source per CIK/CUSIP rather than emitting duplicate rows. Priority is configured per source on the publication. Published views are also rebuilt at server startup so changes take effect without a manual `pvdata publish`.
- Tiingo Stock Tickers can be filtered by asset type (stock, ETF, mutual fund) at subscription time, so a subscription can target only the catalog slice it cares about.
- Editable subscription name. The edit form on the detail page now accepts a new `name` and the API rejects empty values.
- Drag-handle reordering for publication sources. Hold the handle and drag a row to change source priority instead of editing JSON.
- New-subscription form can auto-create a healthchecks.io monitor on submission, picking a slug from the subscription's name and dataset.
- Web UI footer shows the running `pvdata` version and build date; hover the version for the commit hash. The version is sourced from a new `version`/`commit`/`build_date` triple in `/config.json`.

### Changed

- Subscriptions list is sorted by status (active first) then name, and the run-history chart on the detail page uses PrimeVue's charting component for a consistent look with the rest of the UI.
- Editing a subscription's schedule applies live: the cron job is re-registered immediately, the new schedule is pushed to its healthchecks.io monitor (if configured), and timestamps render in the user's chosen timezone.
- Asset-typed publications hide date-range affordances that don't apply to point-in-time asset snapshots.
- The "success" tag in the Last Run column matches the height of the surrounding Active/provider/dataset chips, so rows line up cleanly.

### Fixed

- Per-subscription schema migrations now run eagerly so a freshly-imported subscription doesn't fail its first run with a "column already exists" error. The published-view rebuild path was corrected, the SSE banner reflects the real reconnect status, and the Zacks `RatingKey` parser is hardened against malformed rows.
- Imports of data types without a date column (e.g. assets, market holidays) no longer log spurious "observation overlap" warnings.
- Rows still in the `running` state no longer display a misleading "0ms" duration in the data browser.
- The persisted run log now matches the live SSE stream byte-for-byte; both come from the same capture path.
- `pvdata version` (and the new UI footer) now report the raw `git describe` output (e.g. `v0.5.0` on a tag, `v0.5.0-3-gabcd123` between tags) instead of a synthetic minor-bumped string.

## [0.4.3] - 2026-04-27

### Added

- The web UI now reflects scheduled subscription runs in real time. Open the subscription detail page while a scheduled run is in flight and the run panel auto-attaches to the live event stream — no need to have triggered the run yourself.
- Per-run log capture. Every run's zerolog output is saved as newline-delimited JSON on the `run_history` row and can be reviewed later by clicking the new "Log" button on any row in the run history table. The log viewer parses each line as structured JSON and offers a free-text search, a level filter (info/warn/error/debug), and column-based sort on time, level, or message. Captured logs are retained for 30 days and then cleared automatically by a daily 03:00 NYC sweep.
- Live log streaming. While a run is active, the run panel's new "Logs" tab streams the same structured lines as they are emitted, in addition to the existing per-record summary view.
- Healthchecks.io pings for subscription runs. Subscriptions configured with a `health_check_id` now ping `/start` at the beginning of each run and `/` (success) or `/fail` (failure) at the end, with a one-line body summarizing the source (scheduled/manual), subscription name, observation count, and duration.

### Changed

- The subscription values view (per-data-type tab) now defaults to sorting by `event_date` (or `snapshot_date` for index snapshots) descending, so the most recent rows appear first. Sorting is executed by the database; a new sort-column dropdown and direction toggle in the toolbar let you change column or order without reloading the page client-side.
- Run history is now rendered as a sortable native table with a per-row "Log" action, replacing the previous read-only grid.

## [0.4.2] - 2026-04-27

### Added

- `pvdata subscriptions export` and `pvdata subscriptions import` commands round-trip subscription configurations to/from a TOML file, so a fresh install can be brought up against an existing setup without re-entering each provider config in the TUI.

### Changed

- FRED imports now run incrementally. The provider passes `observation_start` to the FRED API and defaults to a 60-day window when no `--lookback` is provided, instead of refetching the full series history every run. Saves remain idempotent (`ON CONFLICT DO UPDATE`), so revisions within the 60-day window still overwrite the stored value.

### Fixed

- The web UI's run log now shows useful detail for every observation type. FRED, Zacks (ratings, metrics, consensus, estimates), Massive asset records, custom values, and market holidays previously rendered as "observation observation" in the streaming log; they now show ticker, value, and date.
- Massive Stock Tickers and Market Holidays subscriptions now report accurate observation counts and the correct "Completed" / "Failed" status. Previous runs always reported "0 records" and "Failed" because the count tracked an unused slice and the success status was never set on the happy path -- the data was being saved correctly the whole time.

## [0.4.1] - 2026-04-27

### Fixed

- Newly created subscriptions no longer fail their first run with a "column already exists" error. The fresh table is built from the data type's current schema, so the subscription's tracked schema version is now initialized to that current version instead of zero — `RunMigrations` correctly skips migrations the table doesn't need. Existing broken subscriptions can be repaired by setting `schema_version` directly: `UPDATE subscriptions SET schema_version = 1 WHERE id = '<id>'`.
- Playwright now defaults to headless mode. Providers that scrape (Zacks, Nasdaq) previously launched headed Chromium when `playwright.headless` was unset, which fails in containers without an X server. Set `[playwright] headless = false` in your config if you specifically want headed mode for local debugging.

## [0.4.0] - 2026-04-27

### Added

- The web UI's authentication config is now served at runtime from `GET /config.json`, sourced from `auth.issuer`, `auth.client_id`, and `auth.audience` in `~/.pvdata.toml`. The published Docker image works for any OIDC tenant without rebuilding — drop in your config and restart.

### Changed

- Authentication is now provider-agnostic. The backend reads `auth.issuer`, `auth.jwks_url`, and `auth.audience` instead of `auth.domain` / `auth.client_id`, so Auth0, Zitadel, Keycloak, or any standards-compliant OIDC provider works with the same binary. **Migration:** existing deployments using `auth.domain` / `auth.client_id` need to update to the new keys.
- The Docker image now builds the Vue UI as part of the image build, so the published `pennyvault/pvdata` no longer ships with broken asset references.

### Fixed

- `pvdata serve` no longer hangs on shutdown when SSE clients (run-event streams, data-quality run streams) are connected or when a subscription is in-flight. Shutdown is now bounded to roughly 45 seconds worst-case.

## [0.3.0] - 2026-04-26

### Added

- Tiingo subscriptions now accept an `assetTypes` config field — a comma-separated list of pv-data asset codes (e.g. `CS,PS,ETF,ETN,CEF,ADRC,MF`) that limits the EOD import to those types instead of fetching every active asset. Leave it blank to keep the previous behavior.

### Changed

- `pvdata info` now shows each active subscription as a labeled card with Source, Data through, Total records, Securities tracked, Schedule, and Last run. Empty dates render as "no data yet" / "never" instead of "Jan 0001".

### Fixed

- `pvdata migrate init` now runs successfully on a fresh database that doesn't yet have a `market_holidays` table.
- Zacks Backblaze uploads now target the `zacks-rank` bucket (B2 requires bucket names of at least 6 characters; the old `zacks` bucket name was below that limit).

## [0.2.0] - 2026-04-26

### Added

- New `rating`, `estimate`, and `consensus` subscriptions are partitioned by 5-year ranges of `event_date`; new `quote` subscriptions are partitioned monthly. Queries that filter by date prune to the relevant partitions, and partition-level VACUUM / REINDEX is much faster than rewriting the whole table.

### Changed

- Ratings queries are faster due to the `analyst` column being stored as a foreign-key reference to a lookup table
- `pvdata migrate` now reports the actual schema version it advanced to (previously always logged "version 8") and also runs per-subscription table migrations, so dormant subscriptions catch up without needing a manual run
- Tiingo provider now waits through rate-limit windows on HTTP 429 rather than failing the import; aborts only when the daily quota is exhausted

## [0.1.0] - 2026-04-26

### Added

- `pvdata serve` — starts the web UI and subscription scheduler in one command
- `pvdata check` — audits your data for missing values, outliers, stale data, and cross-field inconsistencies; results appear in the web UI Data Quality page
- `pvdata migrate` — applies database migrations without requiring manual SQL
- Data quality summary printed after every import showing how many issues were found
- Web UI for browsing subscriptions, viewing imported data, and reviewing data quality findings
- TradingView provider for index constituents (S&P 500, Russell 1000, etc.)
- iShares ETF provider with historical backfill
- Import from CSV and Parquet files in addition to live provider fetches
- Sharadar SP500 constituent history import
- FIGI resolution for all tickers, including delisted securities
- `--ticker` and `--figi` flags on `pvdata run` to import data for a single security
- Fuzzy suggestions when a ticker or FIGI is not found ("did you mean AAPL?")
- Subscription lookup by name (e.g. `pvdata run "SEC Fundamentals"`) in addition to UUID
- Headless mode: runs without the interactive TUI when no terminal is detected, logging to stderr instead
- Providers supported: Tiingo, Sharadar, SEC, iShares, TradingView, Yahoo Finance, FRED, Nasdaq, Zacks

### Changed

- Bulk imports are substantially faster due to PostgreSQL COPY protocol
- Estimates queries are faster due to the `series` column being stored as an enum
- Log file (`~/.pvdata.log`) now stores structured JSON and is capped at 10 MB

### Fixed

- Import errors (e.g. subscription not found) now appear on the terminal instead of being silently swallowed
- Subscription table in the TUI now renders correctly
- Log viewer no longer truncates long lines
- `pvdata run` now respects the `--lookback` flag for SEC imports
- SEC fundamentals accuracy improvements across many filer types: industrial-financial conglomerates, full-cost E&P energy companies, integrated energy majors, large retailers, restaurant chains, and consumer staples companies

[Unreleased]: https://github.com/penny-vault/pvdata/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/penny-vault/pvdata/compare/v0.4.3...v0.5.0
[0.4.3]: https://github.com/penny-vault/pvdata/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/penny-vault/pvdata/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/penny-vault/pvdata/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/penny-vault/pvdata/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/penny-vault/pvdata/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/penny-vault/pvdata/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/penny-vault/pvdata/releases/tag/v0.1.0
