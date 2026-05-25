# Asset builder: historical backfill design

> **Current home: `provider/catalog/`** (the Historical Asset Catalog
> provider). This document was originally written when the builder lived
> alongside the live Stock Tickers fetch in `provider/massive/`. After
> the split, the live Stock Tickers path was restored to its v0.6.0
> shape in `provider/massive/` and the EOD-driven builder moved to a
> new independent provider at `provider/catalog/`. The two providers
> share no Go-level state. File paths quoted below as
> `provider/massive/...` should be read as `provider/catalog/...` for
> the builder cluster; the originals were either deleted from
> `provider/massive/` or replaced by the restored live code.

This document captures the design for a new asset builder that replaces
the historical backfill side of the existing `historicalWalk → sanitize
→ historicalMap → assetDetails → assignDatesForGroup → publish`
pipeline.

## Scope

- **In scope**: the historical backfill that produces one asset row per
  (ticker, EOD lifecycle range) for every ticker present in the EOD
  parquet archive. Lives in `provider/catalog/` and writes to its own
  per-subscription asset-description table.
- **Out of scope**: the daily flow (today's snapshot, `delistedAssets`,
  the active=true paginated bulk call, per-ticker reference fetches,
  `enrichForPublish` with figi/permid/sec/zacks). That flow stays in
  `provider/massive/` Stock Tickers and writes to its own per-
  subscription table. The operator unions the two via `pvdata publish`
  if a combined view is desired.

## Context

The existing pipeline has accumulated a stack of layered handlers
(`sanitizeWalkComposites`, `chooseListingDate`, `chooseDelistingDate`,
`reconcileBoundary`, `rejectOverlappingMassiveListings`,
`cleanedListDate`, `figi_lifecycle_gate`, the `assetDetail`
constructor, etc.). Each handler is locally reasonable; combined they
produce surprises (LOAN's 5-day-offset failure, DAL's wrong delisted
from reconciler misfires, BBI's earliest-keeper rule fighting with
sanitize, etc.). The phantom-row audit against the 1-minute parquet
on 2004-07-09 surfaced ~397 residual phantoms after multiple rounds
of patches. Continuing to patch wasn't producing convergence.

The new design re-roots the pipeline on a single insight: **the EOD
archive ranges are the source of truth for asset lifecycles.** Massive
metadata fills in details; it does not define lifecycle boundaries.

## Top-level design

For each ticker:

- The **number of EOD ranges** in the archive **is the number of
  lifecycles**.
- The builder produces one asset row per (ticker, EOD range).
- Tickers with no EOD ranges are skipped.

Per asset row:

- `listed` = the EOD range's `Start`
- `delisted` = the EOD range's `End + 1 day`, or `null` if this is the
  most-recent range AND the ticker is in today's active snapshot
- `active` follows from the `delisted` decision
- `composite_figi`, `share_class_figi`, `cik`, `name`,
  `primary_exchange`, etc. come from a single per-ticker reference
  call at a date inside the range

## Builder pseudocode

```
type AssetBuilder struct {
    archive   *EODArchive                              // ticker enumeration + ranges
    fetch     func(ticker, asOf) (*massiveStock, error)
    confirmUS func(figi string) (us bool, ok bool)     // OpenFIGI cross-check
    activeSet map[string]struct{}                      // today's active tickers
    correctCIK func(claimedCIK, ticker, r dateRange) string
    mintFromCIK func(cik, ticker string) string
    mintFromTicker func(ticker, name string) string
}

BuildAll(ctx):
    for ticker in archive.Tickers():
        proposals := buildOne(ctx, ticker)
        for p in proposals:
            publish(ctx, p)               // existing publish channel + SaveDB

buildOne(ctx, ticker):
    ranges := archive.Ranges(ticker)
    if len(ranges) == 0:
        return nil

    out := []
    for i, r in ranges:
        record, err := fetchWithFallback(ticker, r)
        if err != nil:
            // every probed date in r returned 404
            // [OPEN] skip vs construct-from-EOD-only — see Open Questions
            continue

        if record.Type not in trackedTypes:
            continue                       // CS / ADRC / ETF allowlist

        cik := correctCIK(record.CIK, ticker, r)

        isLast := (i == len(ranges) - 1)
        isCurrentlyActive := isLast && _, ok := activeSet[ticker]; ok

        composite := record.CompositeFIGI
        shareClass := record.ShareClassFIGI
        if composite != "" && !confirmUS(composite):
            // foreign-tainted composite (AVP / BBT / GSH / AKS shape)
            composite, _ = mintSynthetic(cik, ticker, record.Name)
            shareClass = ""
        else if composite == "":
            // Massive gave us nothing (DAL historical shape)
            composite, _ = mintSynthetic(cik, ticker, record.Name)
            shareClass = ""

        var delisted time.Time
        var active bool
        if isCurrentlyActive:
            delisted = time.Time{}            // null
            active = true
        else:
            delisted = r.End.AddDate(0, 0, 1)
            active = false

        out = append(out, ProposedAsset{
            Ticker:         ticker,
            Listed:         r.Start,
            Delisted:       delisted,
            Active:         active,
            CompositeFigi:  composite,
            ShareClassFigi: shareClass,
            CIK:            cik,
            Name:           record.Name,
            PrimaryExchange: record.PrimaryExchange,
            AssetType:      record.Type,
        })

    return out

fetchWithFallback(ticker, r):
    // try the bookends first (most likely to resolve), then binary
    // search inward for very-historical lifecycles where Massive's
    // per-ticker reference may not resolve at every date.
    try fetch(ticker, r.End)    ; if ok return
    try fetch(ticker, r.Start)  ; if ok return

    queue := [{r.Start, r.End}]
    iterations := 0
    for len(queue) > 0 && iterations < 12:
        [lo, hi] := pop queue
        if hi.Sub(lo) < 7 days:
            continue
        mid := (lo + hi) / 2
        try fetch(ticker, mid)  ; if ok return
        queue.push({lo, mid}, {mid, hi})
        iterations++

    return error("no resolving date found in range")

mintSynthetic(cik, ticker, name):
    // matches the existing figi/openfigi.go logic
    if cik != "":
        return GenerateSyntheticFIGIFromCIK(cik, ticker), "cik+ticker"
    if ticker != "" && name != "":
        return GenerateSyntheticFIGI(ticker, name), "ticker+name"
    return "", "" // builder caller decides what to do with no FIGI
```

## What this replaces

The new builder subsumes:

- `historicalWalk` (bulk endpoint paginated across every backfill
  date) — gone. Ticker discovery comes from the EOD archive index.
- `historicalMap` keyed by `(ticker, identifier-tuple)` — gone.
  Lifecycles are EOD ranges, not identifier groupings.
- `sanitizeWalkComposites` — folded into builder (OpenFIGI confirm +
  synthetic mint for non-US composites).
- `assetDetails` / `assetDetail` constructor — folded into builder
  (the per-ticker call is now made once per lifecycle).
- `assignDatesForGroup` / `chooseDatesForAsset` / `chooseListingDate`
  / `chooseDelistingDate` — gone. Listed/delisted come from EOD
  range bounds.
- `reconcileBoundary` — gone. Adjacent lifecycles have non-overlapping
  windows by construction (each lifecycle is its own EOD range).
- `rejectOverlappingMassiveListings` — gone. Massive's `list_date` is
  no longer authoritative for listing.
- `cleanedListDate` — gone. Same reason.
- `dropSyntheticDuplicatesOfRealFigi`, `dropSameCompositeFigiDuplicates`,
  `dropOverlappingSyntheticAgainstRealFigi` — gone. The 1-row-per-EOD-
  range data shape prevents the duplicates these handlers cleaned up.

Daily-side handlers (`delistedAssets`, today's snapshot pagination,
`shortDelistedNoCoverageReason`) are not in scope and stay as-is.

See `docs/asset_builder_migration.md` for the full list of accumulated
edge-case handlers in the existing pipeline. Each entry there
describes the failure mode it addresses, and the new builder must
either reproduce the behavior or deliberately replace it.

## Inputs the builder consumes

- **EOD archive index** (`archive.Ranges(ticker)`): authoritative
  source of lifecycle boundaries.
- **Per-ticker reference endpoint**
  (`/v3/reference/tickers/{ticker}?date=X`): the metadata for one
  lifecycle, fetched once per range (with binary-search fallback).
- **OpenFIGI composite validator** (`figi.ValidateCompositeFIGI`):
  one batched lookup at start of the run to identify foreign-tainted
  composites.
- **Today's active snapshot** (single bulk call,
  `?active=true&market=stocks`): used to decide whether the last EOD
  range is currently-active or recently-delisted.
- **SEC ticker → CIK list**: for `correctCIK` to detect Massive's
  occasional misattribution.

## Inputs the builder does NOT consume

- Massive's per-date bulk endpoint walk across thousands of dates.
- Massive's `active=false` paginated bulk endpoint. Inactivity is
  determined by "last EOD range ended before today" and "ticker
  absent from today's active snapshot."
- Walk windows by FIGI / CIK / name. Not built.

## Synthetic FIGI rules

A synthetic FIGI is minted (and the matching share-class FIGI is left
empty) when either:

1. Massive returned a composite_figi but OpenFIGI confirms it as
   non-US. The asset has US EOD bars (by construction — every ticker
   in the builder has EOD ranges), so we mint a synthetic for the
   historical US lifecycle and discard the foreign-tainted FIGI.

2. Massive returned an empty composite_figi.

Synthetic source: `GenerateSyntheticFIGIFromCIK(cik, ticker)` when
CIK is present, else `GenerateSyntheticFIGI(ticker, name)`.

## Open questions to resolve before implementation

- **404 binary-search exhaustion.** What does `buildOne` do when even
  the binary search through `fetchWithFallback` finds no date Massive
  resolves at? Options: (a) skip the lifecycle silently, (b) skip and
  log error, (c) construct the asset from EOD + SEC only (CIK from
  SEC ticker list, name from SEC filings, FIGI as synthetic).
- **Tickers in EOD that 404 at every probed date.** Likely rare. We
  should measure how often this occurs against a small sample before
  picking the policy.
- **Interaction with the existing daily flow.** Daily writes to the
  same table. If the backfill creates a row with `composite_figi =
  PV-synthetic` for a lifecycle that daily later observes is active
  again, what's the merge behavior? (Probably handled by the existing
  upsert on `(ticker, composite_figi)` — a daily insert with a real
  FIGI would land as a separate row, and the synthetic-lifecycle row
  would carry a delisted date.)
- **`active` decision detail.** Right now "active=true only if last
  range AND ticker in today's snapshot." What about a ticker whose
  most recent EOD range ended last week but the ticker is still in
  today's active snapshot? That's a brief gap that probably means
  "the archive is a few days behind today." Need a tolerance or
  defer to "snapshot wins for the last range's active flag."

## State of the repo at design time

- Branch: `main`, 6 commits ahead of `origin/main`.
- Last commit `cc1cec5`: savepoint commit before the redesign. All
  the patches we accumulated this session (gap-tolerance gate,
  sanitize-mint for foreign FIGIs, AssetIndex lifecycle-aware lookup,
  `lifecycleContaining` calendar-day fix, `rejectOverlappingMassive
  Listings` earliest-keeper, `delistedAssets` EOD-derived fallback,
  etc.) are baked into that savepoint and continue to run.
- `docs/asset_builder_migration.md` enumerates the accumulated
  handlers the new builder must preserve.

## Where to start implementing

1. The builder is a new component, alongside the existing pipeline,
   not replacing it in place. Expected location:
   `provider/catalog/asset_builder.go` with a sibling test file.
2. The existing flow continues to run. The builder is invocable via
   a new CLI subcommand (something like `pvdata rebuild` or a flag on
   `pvdata run`) that drives a one-time-or-on-demand backfill against
   the EOD archive.
3. Once the builder produces output that matches or exceeds the
   existing pipeline's quality on the phantom-set audit, the old
   historical-side handlers can be deleted (see `asset_builder_
   migration.md` for the deletion checklist).
