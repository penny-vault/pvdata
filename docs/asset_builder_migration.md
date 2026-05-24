# Asset builder migration: edge-case handlers to preserve

Each entry below is a behavior currently produced by the existing
`historicalMap → sanitize → assetDetails → assignDates → publish` flow.
The new asset builder must produce the same behavior (or a deliberately
different one, documented here) when it replaces this pipeline.

Format: `handler @ file:line` — failure mode it addresses — concrete example.

## Composite / FIGI sanitization

- `sanitizeWalkComposites @ provider/massive/massive.go:1971` — drops
  Massive-substituted foreign composites; keeps ticker alive with a PV
  synthetic when OpenFIGI says non-US but our EOD archive has US bars.
  ATVI on isolated dates (Frankfurt AIY, Euro ATVIEUR) and AVP / BBT /
  GSH (whole-lifecycle foreign-FIGI cases).

- `dropSyntheticDuplicatesOfRealFigi @ provider/massive/massive.go:653`
  — removes synthetic-FIGI siblings of a real-FIGI asset for the same
  ticker so the synthetic mint never displaces a real Bloomberg
  identifier.

- `dropSameCompositeFigiDuplicates @ provider/massive/massive.go:701`
  — collapses multiple entries with the same composite_figi to one row
  (active wins, then most recently updated).

- `dropOverlappingSyntheticAgainstRealFigi @ provider/massive/massive.go:807`
  — among same-ticker rows where one is synthetic and overlaps a real-
  FIGI sibling's window, drops the synthetic.

## Date assignment — single asset

- `chooseListingDate @ provider/massive/date_assignment.go:313` — picks
  listed from Massive ref → EOD first bar → walk first-seen → SEC
  earliest filing → edge-bar fallbacks → ValidFor. Never returns null
  when any signal is present (the listed-never-null invariant).

- `chooseListingDate` previous-lifecycle gate — rejects Massive's
  list_date when a previous EOD lifecycle for the same ticker ends
  after it (OSG / Octave Specialty Group ticker reassignment shape).

- `chooseListingDate` gap-tolerance gate — rejects Massive's list_date
  when the gap to the ticker's own EOD first bar exceeds
  `massiveListDateGapTolerance` (365 days) and EOD coverage was live
  in the gap. iShares Morningstar IM/IL/IS\* rebrand and OTC-to-listed
  transitions (BLFS, DSGR, EVTR, etc.).

- `chooseDelistingDate @ provider/massive/date_assignment.go:435` —
  picks delisted from Massive ref → EOD last bar +1d → walk last-seen
  +1d. For inactive assets only, falls back through edge-bar variants
  to honor the `active=false implies delisted-set` invariant.

- `chooseDatesForAsset @ provider/massive/date_assignment.go:286` —
  pre-filter that drops both Massive ref dates if they are not
  strictly ordered (list_date >= delisted), because either is wrong
  and we cannot tell which.

- `eodFirstBarUsableAsValue / eodLastBarUsableAsValue / walkFirstSeenUsable /
  walkLastSeenUsable @ provider/massive/date_assignment.go` — edge-
  buffer rules that prevent a value at the boundary of coverage from
  being treated as the actual lifecycle endpoint.

## Date assignment — cross-asset reconciliation

- `rejectOverlappingMassiveListings @ provider/massive/date_assignment.go:597`
  — when same-ticker assets share Massive's list_date, keeps it for
  the lifecycle with the earliest EOD first bar; clears it on later
  siblings. BBI's Blockbuster / Brickell case (Massive returns the
  ticker-allocation date 1993-03-16 for both).

- `lifecycleStartBefore / lifecycleStart @ provider/massive/date_assignment.go:650` —
  tie-break when EOD first bar is missing for both candidates (archive
  not mounted, etc.); falls back to walk first-seen.

- `sortIndexedByAssignedListing @ provider/massive/date_assignment.go:246`
  — sorts by chosen listing (not Massive's) so a misattributed CIK
  with old SEC filings does not invert predecessor/successor order.
  Brickell's BBI row carries CIK 0000819050 (Arch Capital historically)
  with 1994 filings; without this sort the order would put Brickell
  ahead of Blockbuster.

- `reconcileBoundary @ provider/massive/date_assignment.go:680` —
  forces predecessor.DelistingDate to one trading day before
  successor's chosen listing when the two windows would otherwise
  conflict.

- `enforceRules @ provider/massive/date_assignment.go:764` — final
  pass: clears delisted if it is not after listing; logs warn when
  active=false has no delisted; logs warn on overlapping windows.

## Lifecycle lookups

- `lifecycleContaining @ provider/massive/figi_lifecycle_gate.go:283` —
  calendar-day comparison (not raw timestamp) so a 2019-12-06T21:27-05:00
  observation is not rejected as past the UTC midnight of 2019-12-06.

- `lifecycleContaining` snap-to-last fallback in `buildDateCandidates @
  provider/massive/date_assignment_wiring.go:158` — when assetDate is
  after the last range's end (active asset with stale archive), use
  the last range as the asset's lifecycle.

- `previousLifecycleEnd @ provider/massive/figi_lifecycle_gate.go:329`
  — feeds the previous-lifecycle gate above.

- `figiLifecycleGate @ provider/massive/figi_lifecycle_gate.go` —
  rejects candidate composites that belong to a different lifecycle
  than the asset under construction.

## Inactive detection / delisting

- `delistedAssets @ provider/massive/massive.go:2867` — queries
  Massive's bulk endpoint with active=false; matches against our DB's
  active rows that fell out of today's snapshot; flips active=false
  with a delisted date.

- `delistedAssets` empty-`delisted_utc` fallback — when Massive
  returns the asset on the inactive endpoint without `delisted_utc`,
  derive the delisting date from the EOD archive's last bar +1d via
  `lastEODBarDelisting`. The active=false-implies-delisted invariant
  is strict: refuse to flip Active when no trading evidence exists.

- `delistedAssets` stale-14-days path — DB-active row that did not
  appear in Massive's inactive lookup but has not been touched for
  >14 days is treated as inactive. Same EOD-derived delisted rule
  applies; refuse the flip when no EOD evidence.

- `shortDelistedNoCoverageReason @ provider/massive/massive.go:923` —
  drops a delisted asset whose listed-to-delisted window has no EOD
  coverage and is shorter than `shortDelistedDurationDays` (180).
  Filters out garbage short-window synthetics.

## Per-ticker detail call

- `assetDetail @ provider/massive/massive.go:3313` — per-ticker
  fetch with `?date=ValidFor`; constructs the publishable asset
  from the response.

- `assetDetail` composite preservation — when the caller's composite
  is a PV synthetic, keep it against Massive's overwrite. Avoids
  re-attaching the foreign-tainted Bloomberg FIGI that sanitize had
  just discarded.

- `assetDetail` share-class clearing — when the chosen composite is
  synthetic, clear ShareClassFigi to keep the identifier pair
  consistent.

- `cleanedListDate @ provider/massive/massive.go:3842` — rejects
  Massive's list_date when it postdates the walk's first-seen for
  this (ticker, composite), since the asset was demonstrably alive
  earlier than Massive claims.

## Walk → asset queue gating

- `filterAssetsByLastUpdated @ provider/massive/massive.go:2601` —
  buckets each walk-discovered asset into:
  - assetDetail (no DB row, OR DB row with stale `last_updated`, OR
    backfill mode),
  - assetUpdate (DB row exists, fresh, but backfill or pre-figi
    seeding wants to refresh), capped at `dailyRunUpdateCap=100` for
    daily runs and uncapped for backfills.
  - skip (fresh DB row, not in backfill).

- `filterAssetsByLastUpdated` missing-branding lane — picks up
  currently-active DB rows whose icon_url / logo_url are NULL and
  queues them for refresh.

- `filterAssetsByLastUpdated` pre-figi assignDates loop — runs
  `assignDates` once for each empty-composite asset BEFORE
  `figi.Enrich`, so figi.Enrich sees the correct DelistingDate (and
  triggers synthetic mint for delisted entities) instead of a row
  that looks alive.

## Ticker / type filtering

- `trackedTypeSet @ provider/massive/massive.go:355` — default
  allowlist `{CS, ADRC, ETF}`, overridable by `--asset-type` flag.

- `filterToTrackedTypes @ provider/massive/massive.go:379` — keeps
  only tracked-type rows; untyped rows fall through to
  `sec.ResolveAssetType` for classification.

## Asset-index lookup

- `AssetIndex.byTickerCIK @ data/asset.go` — 1:many keyed so multi-
  lifecycle (ticker, CIK) pairs (DAL pre/post-bankruptcy) are not
  collapsed. Lookup picks the candidate whose listed/delisted window
  contains the asset's ValidFor; tiebreaks with active-wins-then-
  most-recently-updated when more than one candidate's window
  contains asOf.

- `AssetIndex.byTickerOrgPermID` — same shape, same reason.

- `pickLifecycleMatch @ data/asset.go` — the disambiguation helper
  the two 1:many maps both use.

## CIK correction

- `cik_correction.go` — recovers from Massive misattributing an old
  CIK to a new entity. Uses SEC's ticker list to identify the right
  CIK; updates the asset's CIK field before assignDates runs.

## Save-time guards

- `data/asset.go` SaveDB delisted CASE — when an observation's
  ValidFor predates the stored delisted, keep the stored value
  (prevents older observations from rewinding delisted backwards).
  Note: this is also what kept the wrong delisted=2025-08-29 on BB&T
  pinned until the row was deleted and re-run.

- SaveDB active CASE — same shape for the active flag.

## Invariants the pipeline upholds

- `listed` is never null.
- `active=false` implies `delisted IS NOT NULL`.
- Two same-ticker rows have non-overlapping (listed, delisted] windows.
- A row's delisted is strictly after its listed.

## Tests anchoring the above

Each handler above has at least one unit test in:
- `provider/massive/date_assignment_test.go`
- `provider/massive/walk_windows_internal_test.go`
- `provider/massive/figi_lifecycle_gate_test.go`
- `provider/massive/date_assignment_wiring_test.go`
- `data/asset_test.go`

The migration must keep these green or, for each that the new builder
deliberately changes, replace it with a test of the new behavior and
record why the old behavior was wrong.
