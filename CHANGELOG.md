# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- Ratings queries are faster due to the `analyst` column being stored as a foreign-key reference to a lookup table

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

[Unreleased]: https://github.com/penny-vault/pvdata/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/penny-vault/pvdata/releases/tag/v0.1.0
