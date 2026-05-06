# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- EODHD ([eodhd.com](https://eodhd.com)) is available as a data provider. Subscribe to `Stock Tickers` for the asset catalog (with optional delisted coverage), `EOD` for end-of-day OHLCV with splits and dividends, or `Intraday 1m` for 1-minute bars on a configured ticker list. Configure your API token under the eodhd provider in `pvdata subscribe`. The new `intraday-bar` data type is partitioned monthly.

## [0.5.3] - 2026-05-03

### Added

- iShares index-constituent runs save asset records and daily prices for delisted historical holdings the existing assets table and OpenFIGI cannot identify. The new rows use a synthetic `PVG`-prefixed FIGI so they sort cleanly alongside Bloomberg-issued composites. **Note:** existing iShares subscriptions need to be re-subscribed (or have their `data_types`/`data_tables` extended with `asset-description` and `eod`) for the new rows to land.

### Fixed

- iShares index runs no longer abort an entire index when one historical date contains delisted tickers OpenFIGI cannot resolve. Previously 37 of 38 indexes failed on the first such date and saved nothing for any date; now every date's snapshot and changelog is emitted.

## [0.5.2] - 2026-05-02

### Fixed

- pvindex re-runs rebuild the full history of annual snapshots; previously only the most recent one survived. Re-run with a long lookback to recover.
- Run logs are flushed to the database every five seconds, so a server crash mid-run no longer loses the captured log.

### Changed

- Captured run logs are no longer truncated at 4 MiB.

## [0.5.1] - 2026-05-02

### Changed

- Index snapshots land on fixed calendar dates so re-runs no longer accumulate near-duplicate snapshots.
- `db.max_conns` is configurable (default 25), and connection acquires time out at 30 seconds instead of hanging.

### Removed

- The Nasdaq subscription's `snapshotFrequency` config option; replaced by the fixed annual anchor.

### Fixed

- pvindex skips trading days with incomplete metrics instead of emitting spurious add/remove events.
- Concurrent scheduled runs no longer wedge the server when the connection pool fills.
- Sharadar fundamentals imports no longer drop most historical observations.
- Zacks and Nasdaq scraper log lines now appear in the per-run log viewer.

## [0.5.0] - 2026-04-28

### Added

- Live in-flight runs in the web UI: status chips on the subscriptions list and a run panel with live progress.
- Asset icons and logos shown inline in the data browser.
- Tiingo Stock Tickers can be filtered by asset type at subscription time.
- Editable subscription names.
- Drag-handle reordering for publication sources.
- New-subscription form can auto-create a healthchecks.io monitor.
- Web UI footer shows the running version, build date, and commit hash.

### Changed

- Editing a subscription's schedule applies live: the cron job re-registers and the schedule is pushed to healthchecks.io.

### Fixed

- Newly imported subscriptions no longer fail their first run on a "column already exists" error.
- The persisted run log matches the live SSE stream byte-for-byte.

## [0.4.3] - 2026-04-27

### Added

- The web UI auto-attaches to scheduled runs in flight.
- Per-run log capture with search, level filter, and column sort, retained for 30 days.
- Live log streaming on the run panel while a run is active.
- Healthchecks.io pings at the start, success, and failure of each subscription run.

## [0.4.2] - 2026-04-27

### Added

- `pvdata subscriptions export` and `pvdata subscriptions import` round-trip subscription configurations to and from a TOML file.

### Changed

- FRED imports are incremental (60-day default window) instead of refetching the full history every run.

### Fixed

- Massive Stock Tickers and Market Holidays subscriptions report accurate observation counts and the correct status.

## [0.4.1] - 2026-04-27

### Fixed

- Newly created subscriptions no longer fail their first run with a "column already exists" error. Existing broken subscriptions can be repaired with `UPDATE subscriptions SET schema_version = 1 WHERE id = '<id>'`.
- Playwright defaults to headless mode, so scraping providers work in containers without an X server.

## [0.4.0] - 2026-04-27

### Added

- The web UI's auth config is served at runtime from `GET /config.json`, so the published Docker image works for any OIDC tenant without rebuilding.

### Changed

- Authentication is provider-agnostic. **Migration:** rename `auth.domain` / `auth.client_id` to `auth.issuer`, `auth.jwks_url`, and `auth.audience`.

### Fixed

- `pvdata serve` no longer hangs on shutdown when SSE clients are connected or a subscription is in-flight.

## [0.3.0] - 2026-04-26

### Added

- Tiingo subscriptions accept an `assetTypes` config field to limit the EOD import to specific asset types.

### Fixed

- `pvdata migrate init` runs successfully on a fresh database without a `market_holidays` table.

## [0.2.0] - 2026-04-26

### Added

- Time-partitioned subscription tables for ratings, estimates, consensus, and quotes.

### Changed

- Tiingo waits through rate-limit windows on HTTP 429 instead of failing the import.

## [0.1.0] - 2026-04-26

### Added

- `pvdata serve` — web UI and subscription scheduler in one command.
- `pvdata check` — audits data for missing values, outliers, stale data, and cross-field inconsistencies.
- `pvdata migrate` — applies database migrations without manual SQL.
- Web UI for browsing subscriptions, viewing imported data, and reviewing data quality findings.
- Import from CSV and Parquet files in addition to live provider fetches.
- FIGI resolution for all tickers, including delisted securities.
- `--ticker` and `--figi` flags on `pvdata run` to import data for a single security.
- Subscription lookup by name in addition to UUID.
- Headless mode when no terminal is detected.
- Providers: Tiingo, Sharadar, SEC, iShares, TradingView, Yahoo Finance, FRED, Nasdaq, Zacks.

### Fixed

- Import errors appear on the terminal instead of being silently swallowed.
- `pvdata run` respects the `--lookback` flag for SEC imports.
- SEC fundamentals accuracy improvements across many filer types.

[Unreleased]: https://github.com/penny-vault/pvdata/compare/v0.5.3...HEAD
[0.5.3]: https://github.com/penny-vault/pvdata/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/penny-vault/pvdata/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/penny-vault/pvdata/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/penny-vault/pvdata/compare/v0.4.3...v0.5.0
[0.4.3]: https://github.com/penny-vault/pvdata/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/penny-vault/pvdata/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/penny-vault/pvdata/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/penny-vault/pvdata/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/penny-vault/pvdata/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/penny-vault/pvdata/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/penny-vault/pvdata/releases/tag/v0.1.0
