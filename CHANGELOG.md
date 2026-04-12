# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- `--ticker` and `--figi` flags on `pvdata run` to filter a provider run to a single security for debugging
- Fuzzy match suggestions when a filtered ticker or FIGI is not found ("did you mean AAPL?")
- Subscription lookup by name (e.g. `pvdata run "SEC Fundamentals"`) in addition to UUID prefix
- Non-interactive mode: runs without the TUI when no terminal is detected, logging to stderr instead

### Changed

- Log file (`~/.pvdata.log`) now stores JSON instead of colored console output, and is truncated at 10 MB

### Fixed

- Errors during `pvdata run` (e.g. subscription not found) are now displayed on the terminal instead of silently written to the log file
- Subscription table in the TUI now renders row data correctly
- Log viewer no longer truncates long lines (soft wrapping enabled) or double-colors text
- SEC provider now respects the `--lookback` flag to limit the time range of emitted fundamentals
- SEC field resolution now handles ghost-period date variations across XBRL concepts
