# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.7.1] - 2026-06-07

### Fixed

- Asset rows with NULL descriptive fields no longer break delisted detection or branding refresh during Massive Stock Tickers runs.

## [0.7.0] - 2026-05-28

### Added

- New `catalog` provider with a `Historical Asset Catalog` dataset. It reconstructs per-ticker asset lifecycles from the EOD parquet archive and enriches each lifecycle with reference metadata from Massive, SEC, and OpenFIGI. The live Massive `Stock Tickers` feed continues to own today's snapshot and the daily delisted-detection pass; subscribe to `catalog/Historical Asset Catalog` alongside it to maintain a definitive cross-source asset catalog. Requires `parquet_backup_dir` to be set so the EOD archive is available.
- Sharadar TICKERS backstop for the Historical Asset Catalog. Point `sharadarTickersDir` on the catalog subscription at a directory containing Sharadar TICKERS `*.csv.zst` files and, when a lifecycle's first EOD bar sits on the archive's coverage start and Massive's `list_date` is unusable, Sharadar's `first_price_date` is consulted as a second-tier reference. Leave the field blank to disable.

### Changed

- Massive market holidays are now stored under the canonical `market='us'` label. Previously the download wrote one row per `NYSE` and `NASDAQ` exchange, which conflicted with the `market='us'` filter the trading-day calendar already used. **Note:** existing `market_holidays` rows with `market='NYSE'` or `'NASDAQ'` will be orphaned until the next holiday subscription run refreshes them. The `missing_eod` audit check switches to the new label automatically.
- Massive EOD adjusted-close recomputation parallelizes across composite FIGIs through a worker pool. The math, query shape, and UNNEST update are unchanged; large multi-decade post-fetch passes now finish in a fraction of the previous wall time. Tune via `db.adjust_workers` (default 8).

### Fixed

- Healthchecks.io no longer fires premature failure alerts on runs that go on to succeed. The grace baseline now reflects true wall-clock duration — including the save drain, post-fetch hooks (notably Massive EOD's adjusted-close recomputation), and the PermID backfill — and is tuned off the maximum of the last ten successful runs rather than the average, so a single slow tail run lifts the grace for the next schedule instead of being smoothed away.
- Historical Asset Catalog drops bond and exchange-traded note rows by name shape, catching both the compact `coupon%maturity` stamp (e.g. `7.15%38`, `10.25%2029`) and the verbose `<coupon>% ... Notes Due <YYYY>` form Massive uses for exchange-traded debt. Bond ETFs whose names contain `Bond` or `Notes` as descriptive tokens are kept.
- Historical Asset Catalog name-pattern filter no longer drops closed-end funds (Flaherty & Crumrine, John Hancock Preferred Income, Cohen & Steers REIT and Preferred Income) or Latin American ADRs (Banco Bradesco, Ambev, Itaú) whose names describe preferred holdings or the underlying foreign security.
- OpenFIGI rate limiting is coordinated across every worker in a run through a shared limiter and a fleet-wide 429 backoff, so the parallel asset-detail fan-out stops tripping the published cap and producing the cascading throttling that drove repeated retries.

## [0.6.0] - 2026-05-15

### Added

- EODHD ([eodhd.com](https://eodhd.com)) is available as a data provider. Subscribe to `Stock Tickers` for the asset catalog (with optional delisted coverage), `EOD` for end-of-day OHLCV with splits and dividends, or `Intraday 1m` for 1-minute bars on a configured ticker list. Configure your API token under the eodhd provider in `pvdata subscribe`. The new `intraday-bar` data type is partitioned yearly.
- Massive `1-Minute Bars` dataset ingests 1-minute OHLCV from the S3 flat-files endpoint into the `intraday-bar` ClickHouse table, including pre-market and after-hours rows. Subscribe via `pvdata subscribe` and supply your `flatFilesAccessKey` / `flatFilesSecretKey`.
- Massive `1-Minute Bars (Live)` dataset streams real-time AM bars over the Massive websocket. Schedule it with a weekday cron (e.g. `25 3 * * 1-5` America/New_York); each run holds the connection from morning pre-market through 20:35 ET and disconnects cleanly. Stored in the same `intraday-bar` ClickHouse table as the flat-files variant, and ClickHouse `ReplacingMergeTree` de-duplicates streamed bars against the daily reconciliation pass automatically. Configure `apiKey` and optionally `feed` (`real-time` or `delayed`).
- Massive `Daily Market Summary` REST endpoint fills the gap on days when the flat-file has not yet been published, so same-day EOD requests stop returning empty results.
- Optional flat-file and corporate-actions parquet backups. Set `parquet_backup_dir` in the config and Massive EOD / 1-Minute runs will archive each source file (one parquet per trading day) and the REST splits/dividends responses (one parquet per year) under that directory.
- New audit checks run by `pvdata check`:
  - `missing_eod` flags trading days inside a ticker's expected coverage window where no EOD row exists.
  - `eod_provider_consistency` compares OHLCV across every pair of EOD subscriptions and reports per-field disagreements beyond a price (1 cent or 0.01%) or volume (0.5%) tolerance.
- `clickhouse.disabled` config flag opts out of the ClickHouse backend. Subscriptions with mixed Postgres + ClickHouse data types still run; intraday rows are dropped with a single warning at run start instead of failing on flush.
- `pvdata run --start-date YYYY-MM-DD` scopes a run from an absolute date instead of a relative `--lookback` duration. The two flags are mutually exclusive.
- `pvdata run --end-date YYYY-MM-DD` caps the walk at an explicit upper bound, so targeted historical backfills (e.g. a delisted ticker's lifetime) no longer have to re-walk through today.
- `pvdata import` accepts the parquet backups produced when `parquet_backup_dir` is set on Massive `EOD` or `1-Minute Bars` subscriptions. Pass either individual files or a backup root (e.g. `pvdata import --subscription <name> /backups/<slug>`) and every `<YYYY>/<YYYY-MM-DD>.parquet` under it is replayed into the database. EOD imports automatically join each row against the colocated `splits/<YYYY>.parquet` and `dividends/<YYYY>.parquet` so prices land with the same split factor and dividend cash amount the live fetch wrote.
- `pvdata run --asset-workers N` controls the worker count for the Massive asset discovery (historical reference-tickers walk) and the per-ticker details fan-out. Defaults to 32; long backfills that previously serialised through one goroutine now run with the same rate-limit cap but materially shorter wall time. The viper key `massive.asset_walk_workers` also accepts the value.
- `pvdata figi <ticker> <YYYY-MM-DD>` resolves the composite FIGI that was active for a ticker on a specific date, including for since-delisted securities.
- `pvdata` accepts `--cpuprofile` and `--memprofile` flags on every command for ad-hoc CPU and heap profiling.
- Web UI SQL console queries the ClickHouse backend in addition to Postgres, so intraday-bar tables are now reachable from the browser.
- Scheduled subscription runs that finish in a failed state are now retried automatically. The defaults attempt each scheduled run up to six times (one initial try plus five retries) with a five-minute sleep between attempts, so a transient scraper or upstream-API hiccup no longer requires an operator to re-trigger the run by hand. Override the policy in `.pvdata.toml` with `retry.max_attempts` (set to `1` to disable retries) and `retry.delay` (any Go duration string, e.g. `30s`, `2m`, `1h`). Manual `Run Now` triggers are unaffected.

### Changed

- `pvdata run` defaults to headless logging on stderr; pass `--tui` for the interactive run dashboard. Scheduled runs and `pvdata serve` no longer depend on a TTY check.
- `pvdata run` no longer auto-creates published views or prompts when a data type has multiple sources. Manage published views with `pvdata publish`; the run path only re-applies the SQL of views that already exist.
- The Massive EOD pipeline parallelizes splits and dividends pagination, retries HTTP/2 GOAWAY and mid-stream flat-file failures with exponential backoff, and runs the daily flat-file loop concurrently for ranges over ~1 year of trading days (`massive.flatfile_workers` to tune, default 8).
- Massive EOD and 1-Minute runs read the asset universe from the published `assets` view across every provider, with as-of-date listed/delisted gating, so historical bars for since-delisted tickers resolve correctly.
- The Massive subscribe wizard prompts only for the credentials each dataset uses (REST keys for Stock Tickers / Market Holidays / EOD; flat-files keys for EOD / 1-Minute Bars; `apiKey` + `feed` for 1-Minute Bars (Live)).
- The `intraday-bar` ClickHouse schema applies column-specialized codecs (Gorilla + ZSTD on OHLCV, DoubleDelta + ZSTD on `event_date`, ZSTD on the FIGI column) for materially better compression. Existing tables are not migrated; new tables created after upgrade pick up the codecs automatically.
- `intraday-bar` tables now partition by year (previously by month), keeping the ClickHouse part count low at multi-decade horizons. Existing tables are not migrated; new tables pick up the change automatically.
- Intraday ClickHouse inserts run through a writer pool with batched sends. Tune via `clickhouse.intraday_writers` (default 4) and `clickhouse.intraday_queue_depth` (default 8); minute-bar backfills are now throughput-bound instead of round-trip-bound.
- Massive EOD adjusted-close recomputation batches updates per FIGI via UNNEST and emits a 15-second progress heartbeat (processed / total / elapsed / ETA), eliminating the per-row round-trip that dominated wall time on multi-decade backfills.
- PermID enrichment shares a single API budget and rate limiter per run, short-circuits on HTTP 429, preserves already-resolved PermIDs through asset-detail merges, and the per-run inline and backfill budgets are raised to 2000.
- Web UI throttles per-record SSE events and summarizes intraday-bar counts, so the records tab stays responsive during minute-bar streams.
- `pvdata serve` self-heals missing ClickHouse tables on startup so a fresh install (or a backend that was added to an existing subscription) no longer requires a manual `pvdata migrate`.

### Fixed

- The subscriptions list hides "next run" times for inactive subscriptions instead of showing a stale future timestamp.

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

[Unreleased]: https://github.com/penny-vault/pvdata/compare/v0.7.1...HEAD
[0.7.1]: https://github.com/penny-vault/pvdata/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/penny-vault/pvdata/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/penny-vault/pvdata/compare/v0.5.3...v0.6.0
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
