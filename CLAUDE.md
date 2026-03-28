# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
make build        # Build pvdata binary (injects version via ldflags)
make install      # go install
make lint         # go fmt, go vet, golangci-lint run
make test         # ginkgo run -race ./...
```

Run a single test suite: `ginkgo run -race ./provider/tiingo/`

Run a specific test: `ginkgo run -race --focus "description text" ./package/`

Integration tests use build tag: `ginkgo run -race --tags=integration ./...`

## Architecture

**CLI**: Cobra commands in `cmd/`. Config via Viper (`.pvdata.toml`, env vars). Key config: `db.url`, `openfigi.apikey`, `healthchecks.apikey`.

**Provider system**: Providers implement the `Provider` interface in `provider/provider.go` and register in `provider/discover.go`. Each provider lives in its own sub-package (e.g., `provider/tiingo/`, `provider/sharadar/`). Providers return data through channels of `data.Observation` records. Some providers also implement `FileImporter` for bulk file-based imports.

**Data layer**: `data/` defines domain types (EOD, Fundamental, Rating, etc.) and a `DataType` registry that maps types to their DB schemas and migrations. `library/` manages subscriptions and DB connections (pgx/v5 + pgxpool). Migrations live in `db/migrations/` and are embedded via golang-migrate.

**TUI**: Built with charm/bubbletea v2. Interactive forms use charm/huh v2.

**Web UI**: Vue 3 + Quasar v2 in `web/ui/`. Go backend uses Fiber v2 in `web/`.

**Web scraping**: `playwright_helpers/` wraps playwright-go with stealth evasion injection. To update stealth JS: `npx extract-stealth-evasions@latest` then copy `stealth.min.js` into `playwright_helpers/`.

## Conventions

- Logging: zerolog (`log.Info()`, `log.Error()`, etc.) -- not `fmt.Print` or stdlib `log`
- Testing: Ginkgo v2 + Gomega. Suite files are `*_suite_test.go`. Tests use `Describe`/`It` blocks.
- License: Apache-2.0 SPDX headers on all source files
- Linting: `.golangci.yml` enables `wsl_v5` and `zerologlint` in addition to defaults
