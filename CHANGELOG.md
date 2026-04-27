# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

[Unreleased]: https://github.com/penny-vault/pvdata/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/penny-vault/pvdata/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/penny-vault/pvdata/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/penny-vault/pvdata/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/penny-vault/pvdata/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/penny-vault/pvdata/releases/tag/v0.1.0
