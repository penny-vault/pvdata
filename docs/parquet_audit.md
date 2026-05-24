# Parquet audit: DB vs 1-minute bar parquet comparison

Methodology for comparing the `massive_stock_tickers_asset_description_59f5d`
table against the Massive 1-minute bar parquet files on a target date.

**Do not use ClickHouse for this audit.** The comparison is strictly
between the local 1-minute bar parquet files (read directly from disk)
and the Postgres `massive_stock_tickers_asset_description_59f5d` table.
Routing through ClickHouse adds an indirection that can mask or
introduce discrepancies in the very ticker set the audit is trying to
verify.

Surfaces two failure modes:

- **Missing**: in the parquet, not in the DB. The ticker traded that
  day per 1-minute bars but our DB does not say it was active.
- **Phantom**: in the DB, not in the parquet. Our DB says the ticker
  was active that day but no 1-minute bars exist for it.

Plus invariant checks on the DB.

## One-shot pipeline

```bash
TARGET=2004-07-09
VERSION=v9     # bump per audit run
PARQUET=/Volumes/home/Investing/massive_1_minute_bars_384dc/${TARGET:0:4}/$TARGET.parquet
```

### 1. Parquet ticker set (fixed for the date)

Read the parquet, dedupe, normalize `/` to `.` (the form our DB stores
share-class suffixes in):

```bash
/tmp/parquet_tickers/parquet_tickers "$PARQUET" /tmp/parquet_$TARGET.txt
# Output: one ticker per line, sorted, deduplicated.
```

The `parquet_tickers` tool is a small Go program that reads the
single-day parquet via `github.com/parquet-go/parquet-go`, extracts the
`ticker` column, normalizes `/` → `.`, and emits a sorted unique list.
Source: `/tmp/parquet_tickers/main.go` (separate Go module — not
checked in; rebuild with `go build` if it gets cleaned up).

### 2. DB ticker set (rebuilt per run)

Query the rows whose `listed` / `delisted` window contains the target
date:

```bash
psql "postgres://pennyvault@localhost/pvdb" -At -c "
SELECT REPLACE(ticker,'/','.') AS t
FROM massive_stock_tickers_asset_description_59f5d
WHERE (listed IS NULL OR listed <= '$TARGET')
  AND (delisted IS NULL OR delisted >= '$TARGET')
GROUP BY 1
ORDER BY 1
" > /tmp/massive_assets_${TARGET}_${VERSION}_dotted.txt
```

`GROUP BY` collapses multi-lifecycle rows (e.g., DAL pre/post-bankruptcy
where two rows share the dotted ticker) into one entry per ticker, so
the comm diff works on dotted-ticker presence rather than per-row.

### 3. Two-direction set diff

```bash
comm -23 /tmp/parquet_$TARGET.txt \
        /tmp/massive_assets_${TARGET}_${VERSION}_dotted.txt \
        > /tmp/missing_${TARGET}_${VERSION}.txt

comm -13 /tmp/parquet_$TARGET.txt \
        /tmp/massive_assets_${TARGET}_${VERSION}_dotted.txt \
        > /tmp/massive_only_${TARGET}_${VERSION}.txt

echo "parquet     = $(wc -l < /tmp/parquet_$TARGET.txt)"
echo "db_active   = $(wc -l < /tmp/massive_assets_${TARGET}_${VERSION}_dotted.txt)"
echo "missing     = $(wc -l < /tmp/missing_${TARGET}_${VERSION}.txt)"
echo "phantom     = $(wc -l < /tmp/massive_only_${TARGET}_${VERSION}.txt)"
```

Both files must be sorted (they are, from `ORDER BY` and the
`parquet_tickers` output). `comm -23` keeps lines unique to file 1
(parquet); `comm -13` keeps lines unique to file 2 (DB).

### 4. Delta versus prior version

If a previous audit's outputs are still around (`/tmp/missing_${TARGET}_v8.txt`,
`/tmp/massive_only_${TARGET}_v8.txt`):

```bash
echo "newly missing this version: $(comm -23 /tmp/missing_${TARGET}_${VERSION}.txt /tmp/missing_${TARGET}_v8.txt | wc -l)"
echo "missing resolved since v8 : $(comm -13 /tmp/missing_${TARGET}_${VERSION}.txt /tmp/missing_${TARGET}_v8.txt | wc -l)"
echo "newly phantom this version: $(comm -23 /tmp/massive_only_${TARGET}_${VERSION}.txt /tmp/massive_only_${TARGET}_v8.txt | wc -l)"
echo "phantoms resolved since v8: $(comm -13 /tmp/massive_only_${TARGET}_${VERSION}.txt /tmp/massive_only_${TARGET}_v8.txt | wc -l)"
```

### 5. Invariant health checks

Run after every audit:

```sql
SELECT
  COUNT(*) AS total,
  COUNT(*) FILTER (WHERE listed IS NULL)                      AS listed_null,
  COUNT(*) FILTER (WHERE delisted IS NULL)                    AS delisted_null,
  COUNT(*) FILTER (WHERE active = false AND delisted IS NULL) AS inactive_no_delisted,
  COUNT(*) FILTER (WHERE active = false AND listed   IS NULL) AS inactive_no_listed,
  COUNT(*) FILTER (WHERE active = true  AND listed   IS NULL) AS active_no_listed
FROM massive_stock_tickers_asset_description_59f5d;
```

Expected:

- `listed_null` = 0 (listed must never be null)
- `inactive_no_delisted` = 0 (active=false implies delisted set)
- `inactive_no_listed` = 0
- `active_no_listed` = 0

Any nonzero value here is a rule violation and should be triaged
before continuing.

## Classifying the missing set

To distinguish "real misses we should fix" from "non-tracked types
Massive correctly excludes from our DB," run the classification
harness against Massive's per-ticker reference endpoint:

```bash
MASSIVE_KEY=$(psql "postgres://pennyvault@localhost/pvdb" -At -c \
  "SELECT config->>'apiKey' FROM subscriptions WHERE provider='massive' AND dataset='Stock Tickers'")

mkdir -p /tmp/classify_missing/$VERSION

/tmp/classify_missing/classify \
  -in /tmp/missing_${TARGET}_${VERSION}.txt \
  -out /tmp/classify_missing/$VERSION \
  -date $TARGET \
  -apikey "$MASSIVE_KEY" \
  -workers 20 \
  -rps 25
```

Output buckets in `/tmp/classify_missing/$VERSION/`:

- `bucket_typed_cs.txt` — type=CS per Massive. Real misses; should be
  in the DB.
- `bucket_typed_etf.txt` — type=ETF. Real misses.
- `bucket_typed_non_tracked.txt` — type in {SP, PFD, WARRANT, UNIT,
  INDEX, ADRC, ...} that we don't track. Expected to be missing.
- `bucket_untyped_cik.txt` — empty type, has CIK. Needs SEC follow-up
  to classify as CS-like / fund / other.
- `bucket_untyped_nocik.txt` — empty type, no CIK. Usually non-
  tracked.
- `bucket_not_in_massive.txt` — Massive returns 404 for the per-ticker
  call. Old delisted tickers no longer in Massive's reference.

Source: `/tmp/classify_missing/main.go`. Uses Massive's
`/v3/reference/tickers/{ticker}?date=$TARGET` endpoint.

**Important**: Massive's per-ticker endpoint returns 404 for delisted
tickers when called without a `date=` parameter. Always pass `date=`
matching the audit's target date so old tickers resolve.

## Debugging individual phantoms

For each phantom ticker, fetch context from Massive + EOD archive:

```bash
shuf -n 100 /tmp/massive_only_${TARGET}_${VERSION}.txt > /tmp/phantom100.txt

/tmp/phantom_debug/phantom_debug \
  -tickers /tmp/phantom100.txt \
  -apikey "$MASSIVE_KEY" \
  -date $TARGET \
  > /tmp/phantom100_context.tsv
```

The output is a TSV with `ticker`, Massive per-ticker fields, EOD
archive range count, EOD first/last bars, and the full list of EOD
ranges for the ticker. Source: `/tmp/phantom_debug/main.go`.

Merge with DB context for full diagnosis:

```sql
CREATE TEMP TABLE _ph (dotted text);
COPY _ph FROM '/tmp/phantom100.txt';

SELECT
  p.dotted,
  s.composite_figi,
  s.active,
  s.listed::date,
  COALESCE(s.delisted::date::text, 'null') AS delisted,
  COALESCE(s.cik, '') AS cik,
  left(s.name, 50) AS name
FROM _ph p
JOIN massive_stock_tickers_asset_description_59f5d s
  ON REPLACE(s.ticker,'/','.') = p.dotted
WHERE (s.listed IS NULL OR s.listed <= '2004-07-09')
  AND (s.delisted IS NULL OR s.delisted >= '2004-07-09')
ORDER BY p.dotted, s.listed;
```

## Phantom shapes observed during the 2004-07-09 audit

For reference, these are the failure modes the audit surfaced (most
addressed by the redesign in `asset_builder_design.md`):

- **Foreign-FIGI rebrand** (iShares Morningstar IM/IL/IS\* family):
  Massive returns `list_date` from a predecessor product, listing
  the new ticker as much older than its actual first bar.
- **OTC-to-listed transition** (BLFS, DSGR, EVTR, etc.): Massive's
  `list_date` reflects the entity's existence; first US-exchange EOD
  bar is years later.
- **Same-CIK bankruptcy split** (DAL): two distinct trading
  instruments under the same legal entity, same CIK, with a gap
  between EOD lifecycles.
- **Different-CIK ticker reuse** (BBI Blockbuster → Brickell): two
  unrelated entities sharing the same Massive-reported `list_date` =
  the original ticker-allocation date.
- **5-day walk-firstSeen vs EOD start mismatch** (LOAN): the walk
  surfaces the ticker a few days before EOD bars start; the strict
  lifecycle-containment lookup misses, EOD candidates land empty.

## Tools and source locations

- `parquet_tickers`: extracts ticker column from a single-day parquet.
  Source `/tmp/parquet_tickers/main.go` (one-off Go module).
- `classify_missing/classify`: classifies a ticker list against
  Massive's per-ticker reference. Source
  `/tmp/classify_missing/main.go`.
- `phantom_debug`: fetches Massive + EOD context for a ticker list.
  Source `/tmp/phantom_debug/main.go`.
- `verify_listing` (used during single-ticker verification, not
  audits): simulates the algorithm against real EOD + Massive data
  for a ticker list. Source `/tmp/verify_listing/main.go`.
- `dump_ranges`: dumps EOD ranges for a list of tickers from the
  archive index parquet. Source `/tmp/verify_listing/dump_ranges.go`
  (built with `-tags dumpranges`).

These tools all read directly from local parquet files (no ClickHouse
server) and from Massive's HTTP API. None of them are checked into
the repo; rebuild from the source files in `/tmp/` if they're cleaned
up.

## Versioning convention

Each audit run produces a triplet:

- `/tmp/missing_${TARGET}_${VERSION}.txt`
- `/tmp/massive_only_${TARGET}_${VERSION}.txt`
- `/tmp/massive_assets_${TARGET}_${VERSION}_dotted.txt`

VERSION is incremented per audit. Keep prior versions around to
compute deltas.

Historical numbers from the 2004-07-09 audit:

| version | db_active | missing | phantom | notes                          |
|---------|-----------|---------|---------|--------------------------------|
| v6      | 7,058     | 1,522   | 1,041   | pre-fix baseline               |
| v7      | 6,405     | 1,524   | 390     | sanitize + gap-tolerance fix   |
| v8      | 6,566     | 1,370   | 397     | + AssetIndex lifecycle-aware   |
