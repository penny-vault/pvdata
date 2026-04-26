# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.0] - 2026-04-26

### Added

- Data quality framework: inline validators run during `SaveObservations`, audit runner with checkpoint support, five layers of checks (sanity, cross-field, statistical outlier, coverage/staleness, cross-type consistency)
- `pvdata check` command for running data quality audits
- Data Quality page and API endpoints in web UI
- Post-run data quality summary line after imports
- Web UI: Vue 3 + PrimeVue frontend with SPA serving embedded in Go binary, RevoGrid tables, colored chips, data-type-aware search, and wizard redesign
- `pvdata serve` command combining web server and subscription scheduler
- `pvdata migrate` command for database migrations
- TradingView provider for index constituents with full catalog
- iShares provider with lookback-based backfill and CSV API
- Streaming file import pipeline with CSV and Parquet variants
- Sharadar SP500 file import with nullable parquet column support
- FIGI resolution for all tickers including delisted equities and synthetic FIGI generation
- `AllAssets` function to query both active and delisted assets
- `tradingDays` helper to query NYSE trading days from database
- Complete backend API layer
- Provider sub-packages: tiingo, sharadar, yfinance, fred, nasdaq, zacks, ishares, tradingview, massive, legacy
- `--ticker`/`--figi` flags on `pvdata run` to filter a provider run to a single security for debugging
- Fuzzy match suggestions when a filtered ticker or FIGI is not found ("did you mean AAPL?")
- Subscription lookup by name (e.g. `pvdata run "SEC Fundamentals"`) in addition to UUID prefix
- Non-interactive mode: runs without the TUI when no terminal is detected, logging to stderr instead
- Index snapshot model as single JSONB record per snapshot
- Stealth JS embedded directly (replacing go-rod/stealth dependency)

### Changed

- Providers reorganized into sub-packages under `provider/`
- Index data types split into `IndexSnapshotKey` and `IndexChangelogKey`
- Index schema renamed `index_name` to `index_ticker`
- Bulk inserts use PostgreSQL COPY protocol for significantly higher throughput
- iShares provider switched from Playwright to CSV API
- Upgraded to charmbracelet/bubbletea v2 and charmbracelet/huh v2
- Estimates `series` column converted from text to enum for query performance
- Log file (`~/.pvdata.log`) now stores JSON instead of colored console output, and is truncated at 10 MB

### Fixed

- Errors during `pvdata run` now displayed on the terminal instead of silently written to the log file
- Subscription table in the TUI now renders row data correctly
- Log viewer no longer truncates long lines or double-colors text
- SEC provider now respects the `--lookback` flag
- SEC field resolution now handles ghost-period date variations across XBRL concepts
- SEC: extensive fixes for industrial-financial (CAT-style), full-cost E&P, integrated energy (XOM-style), retailer (WMT-style), restaurant (MCD/TXRH-style), consumer staples (KO-style), and general-purpose filer patterns
- SEC: correct per-share metrics, cash flow classification, balance sheet resolution, deferred tax, impairment, and shares calculation across dozens of filer patterns
- SEC: filter `LoadCIKMapFromDB` to active assets only
- SEC: tighten balance-sheet concept staleness to latest 10-Q

[Unreleased]: https://github.com/penny-vault/pvdata/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/penny-vault/pvdata/releases/tag/v0.1.0
