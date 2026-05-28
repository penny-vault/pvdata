// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/provider"
	"github.com/penny-vault/pvdata/provider/sec"
)

// builderBinarySearchMaxIterations bounds fetchWithFallback's binary
// search after the two range bookends have been tried. The split halves
// the search window each iteration; twelve splits cover every range
// finer than a single day before the loop bottoms out, which is well
// past any realistic historical lifecycle length.
const builderBinarySearchMaxIterations = 12

// builderBinarySearchMinSpanDays stops the binary search once the
// current candidate window shrinks below this many days. Massive's
// per-ticker reference snapshots are recorded with day granularity;
// continuing to split inside a one-week window only repeats the same
// probe dates.
const builderBinarySearchMinSpanDays = 7

// defaultBuilderFetchWorkerCount is the fallback worker count for
// Phase 1 per-ticker reference fetches when the operator has not set
// `--asset-workers` (the `massive.asset_walk_workers` viper key).
// Workers all share api.limiter so the aggregate request rate stays
// under the configured cap regardless of how many goroutines are
// draining the queue.
const defaultBuilderFetchWorkerCount = 16

// builderFetchWorkerCount returns the per-ticker reference-fetch
// worker pool size, sourced from the `massive.asset_walk_workers`
// viper key (bound to the `--asset-workers` flag) and falling back
// to defaultBuilderFetchWorkerCount when unset or non-positive.
func builderFetchWorkerCount() int {
	n := viper.GetInt("massive.asset_walk_workers")
	if n <= 0 {
		return defaultBuilderFetchWorkerCount
	}

	return n
}

// builderUSExchangeCode is the OpenFIGI ExchangeCode that identifies a
// US-listed composite. Any other value (e.g. "GR" for Frankfurt, "FP"
// for Paris) means the composite belongs to a foreign listing and the
// builder mints a synthetic FIGI for the US lifecycle instead.
const builderUSExchangeCode = "US"

// errBuilderNoResolvingDate is returned by fetchWithFallback when every
// probed date inside a lifecycle range comes back as a 404 from
// Massive's per-ticker reference endpoint. The caller skips that
// lifecycle rather than attempting to fabricate metadata.
var errBuilderNoResolvingDate = errors.New("massive builder: no resolving date found in lifecycle range")

// proposedAsset captures the per-(ticker, lifecycle) state produced by
// Phase 1 of the builder. The raw Massive response is kept verbatim so
// Phase 3 can apply the OpenFIGI cross-check and synthetic mint rules
// once all responses are in hand. isLast marks the most-recent
// lifecycle for the ticker — only it can pair with today's active
// snapshot to leave delisted unset.
type proposedAsset struct {
	ticker  string
	rng     dateRange
	isLast  bool
	probeAt time.Time
	record  massiveStock
}

// AssetBuilder is the EOD-driven historical asset builder. It walks
// every ticker present in the EOD archive index and emits one asset
// observation per EOD lifecycle range. The number of EOD ranges for a
// ticker IS the number of lifecycles; Massive's per-ticker reference
// endpoint fills in metadata (FIGI, CIK, name, exchange) but never
// defines the lifecycle boundaries themselves. See
// docs/asset_builder_design.md for the full rationale.
type AssetBuilder struct {
	api      *massiveAssetFetcher
	archive  *EODArchive
	tracked  map[string]struct{}
	todayIDs map[string]struct{}
	sharadar *sharadarTickerIndex // nil disables Sharadar enrichment
}

// NewAssetBuilder returns an AssetBuilder bound to the given fetcher.
// The fetcher supplies the REST client, the OpenFIGI rate limiter
// indirectly (through figi.ValidateCompositeFIGI), and the publish
// channel SaveObservations drains. The fetcher must have a usable
// EOD archive (parquet_backup_dir configured); BuildAll returns an
// error when the archive is missing. A nil sharadar index disables
// Sharadar backstop enrichment.
func NewAssetBuilder(api *massiveAssetFetcher, tracked map[string]struct{}, todayActive map[string]struct{}, sharadar *sharadarTickerIndex) *AssetBuilder {
	return &AssetBuilder{
		api:      api,
		tracked:  tracked,
		todayIDs: todayActive,
		sharadar: sharadar,
	}
}

// BuildAll runs the three-phase build: discover (ticker, range)
// proposals by fetching the per-ticker reference endpoint, validate
// every observed composite_figi against OpenFIGI to identify foreign-
// tainted FIGIs, then finalize and publish one asset row per surviving
// proposal. Returns an error only when the EOD archive is unavailable
// or the context is cancelled; per-ticker errors are logged and the
// affected lifecycle is skipped.
func (b *AssetBuilder) BuildAll(ctx context.Context) error {
	logger := zerolog.Ctx(ctx)

	b.archive = b.api.eodArchiveForRun()
	if b.archive == nil {
		return errors.New("massive builder: EOD archive not available (parquet_backup_dir not configured or empty)")
	}

	tickers := b.archive.AllTickers()

	// Narrow to a single ticker when --ticker is set so a debug /
	// targeted backfill does not fetch tens of thousands of unrelated
	// rows. --figi is not honoured here because the EOD archive index
	// is keyed by ticker; a FIGI scope is naturally satisfied by the
	// per-lifecycle FIGI propagation in finalize().
	if tickerFilter, _ := provider.SecurityFilterFromContext(ctx); tickerFilter != "" {
		filtered := make([]string, 0, 1)

		for _, t := range tickers {
			if strings.EqualFold(t, tickerFilter) {
				filtered = append(filtered, t)
			}
		}

		tickers = filtered

		if len(tickers) == 0 {
			logger.Warn().
				Str("Ticker", tickerFilter).
				Msg("massive builder: --ticker filter matched no EOD-archive tickers; nothing to do")

			return nil
		}
	}

	logger.Info().
		Int("Tickers", len(tickers)).
		Int("Workers", builderFetchWorkerCount()).
		Msg("massive builder: phase 1 — per-ticker reference fetch")

	phase1Start := time.Now()

	proposals, err := b.discoverProposals(ctx, tickers)
	if err != nil {
		return err
	}

	logger.Info().
		Int("Tickers", len(tickers)).
		Int("Proposals", len(proposals)).
		Dur("Elapsed", time.Since(phase1Start).Round(time.Second)).
		Msg("massive builder: phase 1 complete")

	logger.Info().Int("Proposals", len(proposals)).Msg("massive builder: phase 2 — OpenFIGI composite validation")

	phase2Start := time.Now()

	nonUSFigis := b.identifyNonUSFigis(ctx, proposals)

	logger.Info().
		Int("ProbedFigis", len(proposals)).
		Int("NonUSFigis", len(nonUSFigis)).
		Dur("Elapsed", time.Since(phase2Start).Round(time.Second)).
		Msg("massive builder: phase 2 complete")

	logger.Info().Int("Proposals", len(proposals)).Msg("massive builder: phase 3 — finalize and publish")

	phase3Start := time.Now()

	published := 0
	skipped := 0

	for _, p := range proposals {
		if err := ctx.Err(); err != nil {
			return err
		}

		asset := b.finalize(ctx, p, nonUSFigis)
		if asset == nil {
			skipped++
			continue
		}

		b.api.publish(ctx, asset)

		published++
	}

	logger.Info().
		Int("Published", published).
		Int("Skipped", skipped).
		Dur("Elapsed", time.Since(phase3Start).Round(time.Second)).
		Msg("massive builder: phase 3 complete")

	return nil
}

// discoverProposals fans Phase 1 fetches across workers. Each (ticker,
// range) is one work item; fetchWithFallback handles bookend +
// binary-search retries inside the worker so a single 404 does not
// trigger an extra hop through the queue. Failures are logged and the
// lifecycle skipped — they are not propagated as errors.
func (b *AssetBuilder) discoverProposals(ctx context.Context, tickers []string) ([]*proposedAsset, error) {
	type job struct {
		ticker string
		rng    dateRange
		isLast bool
	}

	// Count total lifecycle ranges up front so the heartbeat can show
	// progress as fraction-done and compute an ETA.
	totalJobs := 0

	for _, ticker := range tickers {
		totalJobs += len(b.archive.TrackableRanges(ticker))
	}

	jobs := make(chan job, builderFetchWorkerCount()*4)

	var (
		resultsMu sync.Mutex
		results   []*proposedAsset
	)

	var (
		fetched404   atomic.Int64
		fetchedOK    atomic.Int64
		fetchedError atomic.Int64
	)

	g, gctx := errgroup.WithContext(ctx)

	// Heartbeat goroutine: emits a progress line every 30 seconds so
	// a 30k-ticker run is not silent for an hour. Exits when ctx is
	// cancelled or the done channel closes after g.Wait() returns.
	heartbeatDone := make(chan struct{})
	heartbeatStart := time.Now()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-heartbeatDone:
				return
			case <-gctx.Done():
				return
			case <-ticker.C:
				ok := fetchedOK.Load()
				skipped := fetched404.Load()
				errored := fetchedError.Load()
				done := ok + skipped + errored
				elapsed := time.Since(heartbeatStart)

				eta := "—"

				if done > 0 && int(done) < totalJobs {
					perJob := elapsed / time.Duration(done)
					remaining := int64(totalJobs) - done
					eta = (perJob * time.Duration(remaining)).Round(time.Second).String()
				}

				zerolog.Ctx(ctx).Info().
					Int64("Done", done).
					Int("Total", totalJobs).
					Int64("Published", ok).
					Int64("SkippedNoRecord", skipped).
					Int64("Errored", errored).
					Str("Elapsed", elapsed.Round(time.Second).String()).
					Str("ETA", eta).
					Msg("massive builder: phase 1 progress")
			}
		}
	}()

	for range builderFetchWorkerCount() {
		g.Go(func() error {
			for j := range jobs {
				probedAt, record, err := b.fetchWithFallback(gctx, j.ticker, j.rng)
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return err
					}

					if errors.Is(err, errBuilderNoResolvingDate) {
						fetched404.Add(1)

						// Common for stray bars in the parquet archive
						// that Massive's reference catalog no longer
						// knows about (AAZST-style placeholder rows,
						// AEI's single 2004 bar before Alset Inc. took
						// the ticker, etc.). Nothing for the operator
						// to fix, so log at debug.
						zerolog.Ctx(gctx).Debug().
							Str("Ticker", j.ticker).
							Time("RangeStart", j.rng.Start).
							Time("RangeEnd", j.rng.End).
							Msg("massive builder: no Massive reference record for any probed date in lifecycle range; skipping")

						continue
					}

					fetchedError.Add(1)

					zerolog.Ctx(gctx).Error().
						Err(err).
						Str("Ticker", j.ticker).
						Time("RangeStart", j.rng.Start).
						Time("RangeEnd", j.rng.End).
						Msg("massive builder: per-ticker reference fetch failed; skipping lifecycle")

					continue
				}

				fetchedOK.Add(1)

				p := &proposedAsset{
					ticker:  j.ticker,
					rng:     j.rng,
					isLast:  j.isLast,
					probeAt: probedAt,
					record:  record,
				}

				resultsMu.Lock()

				results = append(results, p)
				resultsMu.Unlock()
			}

			return nil
		})
	}

	g.Go(func() error {
		defer close(jobs)

		for _, ticker := range tickers {
			ranges := b.archive.TrackableRanges(ticker)
			if len(ranges) == 0 {
				continue
			}

			for i, r := range ranges {
				select {
				case jobs <- job{ticker: ticker, rng: r, isLast: i == len(ranges)-1}:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
		}

		return nil
	})

	err := g.Wait()

	close(heartbeatDone)

	if err != nil {
		return nil, err
	}

	zerolog.Ctx(ctx).Info().
		Int("Total", totalJobs).
		Int64("FetchedOK", fetchedOK.Load()).
		Int64("Fetched404", fetched404.Load()).
		Int64("Errored", fetchedError.Load()).
		Str("Elapsed", time.Since(heartbeatStart).Round(time.Second).String()).
		Msg("massive builder: phase 1 fetch summary")

	return results, nil
}

// fetchWithFallback resolves one (ticker, lifecycle) into a Massive
// per-ticker reference record. The endpoint requires a date= parameter
// for delisted tickers; without it the API returns NOT_FOUND.
//
// The algorithm probes the lifecycle Start first because each EOD
// lifecycle is bounded to a single entity, and the Start anchors the
// historical entity: for a reused ticker the Start of an old lifecycle
// is in the original entity's era, while the End may abut a reuse
// boundary. Start-first prevents the modern entity's type/CIK from
// leaking into a historical lifecycle. If the Start probe returns an
// incomplete record (missing type or CIK), the End probe and a
// bounded binary search inward fill in the gaps. Records are scored by
// completeness ((type ? 2 : 0) + (cik ? 1 : 0)); the search short-
// circuits on a complete (score 3) record and otherwise returns the
// best-scoring record seen. Returns errBuilderNoResolvingDate when no
// probed date resolves at all.
func (b *AssetBuilder) fetchWithFallback(ctx context.Context, ticker string, r dateRange) (time.Time, massiveStock, error) {
	tried := make(map[string]struct{}, builderBinarySearchMaxIterations+2)

	var (
		bestAt     time.Time
		bestRecord massiveStock
		bestScore  = -1
	)

	// probe issues one fetch (skipping dates already tried), updates the
	// best-seen record, and reports whether the result is "complete"
	// (has both type and CIK). The caller short-circuits on a complete
	// result; otherwise it keeps probing in case a later date returns a
	// stronger record.
	probe := func(at time.Time) (bool, error) {
		key := at.Format("2006-01-02")
		if _, seen := tried[key]; seen {
			return false, nil
		}

		tried[key] = struct{}{}

		record, found, err := b.fetchAt(ctx, ticker, at)
		if err != nil {
			return false, err
		}

		if !found {
			return false, nil
		}

		score := scoreMassiveRecord(record)
		if score > bestScore {
			bestAt = at
			bestRecord = record
			bestScore = score
		}

		return score == massiveRecordCompleteScore, nil
	}

	if complete, err := probe(r.Start); err != nil {
		return time.Time{}, massiveStock{}, err
	} else if complete {
		return bestAt, bestRecord, nil
	}

	if complete, err := probe(r.End); err != nil {
		return time.Time{}, massiveStock{}, err
	} else if complete {
		return bestAt, bestRecord, nil
	}

	type window struct{ lo, hi time.Time }

	queue := []window{{lo: r.Start, hi: r.End}}

	for iter := 0; iter < builderBinarySearchMaxIterations && len(queue) > 0; iter++ {
		w := queue[0]
		queue = queue[1:]

		if w.hi.Sub(w.lo) < time.Duration(builderBinarySearchMinSpanDays)*24*time.Hour {
			continue
		}

		midDays := int(w.hi.Sub(w.lo)/(24*time.Hour)) / 2
		mid := w.lo.AddDate(0, 0, midDays)

		complete, err := probe(mid)
		if err != nil {
			return time.Time{}, massiveStock{}, err
		}

		if complete {
			return bestAt, bestRecord, nil
		}

		queue = append(queue, window{lo: w.lo, hi: mid}, window{lo: mid, hi: w.hi})
	}

	if bestScore < 0 {
		return time.Time{}, massiveStock{}, errBuilderNoResolvingDate
	}

	return bestAt, bestRecord, nil
}

// massiveRecordCompleteScore is the score a Massive per-ticker record
// earns when both `type` and `cik` are populated. The search in
// fetchWithFallback short-circuits as soon as a probe returns a record
// with this score.
const massiveRecordCompleteScore = 3

// scoreMassiveRecord ranks a Massive per-ticker record by how much
// useful classification information it carries. Type is worth more than
// CIK because finalize uses type directly while CIK only enables a
// downstream SEC lookup.
func scoreMassiveRecord(r massiveStock) int {
	score := 0

	if strings.TrimSpace(r.Type) != "" {
		score += 2
	}

	if strings.TrimSpace(r.CIK) != "" {
		score++
	}

	return score
}

// fetchAt issues one GET /v3/reference/tickers/{ticker}?date=at and
// returns the deserialized record. found=false signals a 404 (the
// caller can move on to the next probe date); any other non-2xx
// response surfaces as an error.
func (b *AssetBuilder) fetchAt(ctx context.Context, ticker string, at time.Time) (massiveStock, bool, error) {
	if err := b.api.limiter.Wait(ctx); err != nil {
		return massiveStock{}, false, err
	}

	if err := b.api.cooldown.Wait(ctx); err != nil {
		return massiveStock{}, false, err
	}

	url := fmt.Sprintf("https://api.massive.com/v3/reference/tickers/%s", pvTicker2MassiveTicker(ticker))

	var respContent massiveResponse

	resp, err := b.api.client.R().
		SetContext(ctx).
		SetResult(&respContent).
		SetQueryParam("date", at.Format("2006-01-02")).
		Get(url)
	if err != nil {
		return massiveStock{}, false, fmt.Errorf("massive builder: GET %s @ %s: %w", url, at.Format("2006-01-02"), err)
	}

	if resp.StatusCode() == http.StatusNotFound {
		return massiveStock{}, false, nil
	}

	if resp.StatusCode() >= 300 {
		return massiveStock{}, false, fmt.Errorf("%w (%d): %s", ErrInvalidStatusCode, resp.StatusCode(), string(resp.Body()))
	}

	if respContent.Results == nil {
		return massiveStock{}, false, nil
	}

	var record massiveStock
	if err := json.Unmarshal(*respContent.Results, &record); err != nil {
		return massiveStock{}, false, fmt.Errorf("massive builder: decode response for %s @ %s: %w", ticker, at.Format("2006-01-02"), err)
	}

	if strings.TrimSpace(record.Ticker) == "" {
		return massiveStock{}, false, nil
	}

	return record, true, nil
}

// identifyNonUSFigis runs the batched OpenFIGI confirmation pass for
// every distinct non-synthetic composite_figi seen in Phase 1.
// Composites OpenFIGI reports with an ExchangeCode other than "US" are
// returned as the non-US set; composites OpenFIGI does not know about
// (delisted / evicted) are NOT in the set so they pass through
// unchanged. Synthetic FIGIs are never validated.
func (b *AssetBuilder) identifyNonUSFigis(ctx context.Context, proposals []*proposedAsset) map[string]struct{} {
	figiSet := make(map[string]struct{}, len(proposals))

	for _, p := range proposals {
		f := strings.TrimSpace(p.record.CompositeFIGI)
		if f == "" || figi.IsSyntheticFIGI(f) {
			continue
		}

		figiSet[f] = struct{}{}
	}

	if len(figiSet) == 0 {
		return map[string]struct{}{}
	}

	figis := make([]string, 0, len(figiSet))
	for f := range figiSet {
		figis = append(figis, f)
	}

	confirmed := figi.ValidateCompositeFIGI(ctx, figis)

	nonUS := make(map[string]struct{}, len(confirmed))

	for f, ofa := range confirmed {
		if ofa == nil {
			continue
		}

		if ofa.ExchangeCode != builderUSExchangeCode {
			nonUS[f] = struct{}{}
		}
	}

	return nonUS
}

// finalize applies the synthetic-FIGI mint rules, CIK correction,
// asset-type filter, and lifecycle-bound dates to one proposal. Returns
// nil when the proposal must be dropped (asset type not tracked, no
// composite FIGI can be produced, etc.). The returned asset is ready
// for publish + SaveDB.
func (b *AssetBuilder) finalize(ctx context.Context, p *proposedAsset, nonUSFigis map[string]struct{}) *data.Asset {
	logger := zerolog.Ctx(ctx)

	// Type-aware name filter: skips "preferred"/"pfd" patterns for
	// fund/ADR types (CEFs and ADRs often describe holdings or the
	// underlying), keeps all other patterns for every type.
	if reason, drop := nameSaysNonTradableForType(p.record.Name, p.record.Type); drop {
		logger.Debug().
			Str("Ticker", p.ticker).
			Str("Name", p.record.Name).
			Str("MassiveType", p.record.Type).
			Str("Reason", reason).
			Msg("massive builder: dropping lifecycle by name pattern (test symbol, warrant, preferred, when-issued, etc.)")

		return nil
	}

	// Massive's `type` field is the authoritative classification once
	// the name check has cleared. When type is set we just compare it
	// against the tracked set. Only when the field is empty (common
	// for pre-2010 delisted records) do we fall back to the
	// ticker-shape heuristic and the SEC form vote.
	assetType := data.AssetType(strings.TrimSpace(p.record.Type))

	if assetType == "" {
		if reason, drop := tickerSaysNonTradable(&data.Asset{Ticker: p.ticker}); drop {
			logger.Debug().
				Str("Ticker", p.ticker).
				Str("Reason", reason).
				Msg("massive builder: dropping untyped lifecycle by ticker shape (warrant suffix, lowercase placeholder marker)")

			return nil
		}

		if cik := strings.TrimSpace(p.record.CIK); cik != "" {
			year := p.rng.Start.Year()

			resolved, _, ok := sec.ResolveAssetTypeWithCIKCorrection(ctx, cik, p.ticker, p.record.Name, year)
			if ok {
				assetType = resolved
			}
		}

		// Last-resort fallback: nothing classified the lifecycle but
		// it survived the name and ticker filters, so it is plausibly a
		// real security we just cannot tag. Publish with the UNK type
		// so the row is in the catalog and visible for later review,
		// rather than dropping it silently.
		if assetType == "" {
			assetType = data.UnknownAsset

			logger.Debug().
				Str("Ticker", p.ticker).
				Str("Name", p.record.Name).
				Time("RangeStart", p.rng.Start).
				Time("RangeEnd", p.rng.End).
				Msg("massive builder: no classifier produced a type; publishing as UNK")
		}
	}

	// SEC override: Massive's `type` is keyed off the ticker rather than
	// the entity, so reused tickers leak the modern entity's type back
	// onto a historical lifecycle (PILL was ProxyMed CS in 2004; today
	// PILL belongs to a Direxion ETF — Massive returns ETF at every
	// historical date). When Massive classifies a lifecycle as an
	// investment vehicle but SEC's submissions for the historical CIK
	// show exclusively operating-company forms (10-K/10-Q/20-F/40-F with
	// zero N-series filings), trust SEC. Narrowly scoped: any fund-form
	// evidence aborts the override, so real CEFs/ETFs/MFs whose SEC
	// filings include N-CSR/N-2/N-1A/NPORT pass through untouched.
	switch assetType {
	case data.ETF, data.MutualFund, data.CEF, "FUND":
		if cik := strings.TrimSpace(p.record.CIK); cik != "" {
			if overrideType, ok := sec.OverrideTypeIfExclusivelyOperating(ctx, cik); ok {
				logger.Debug().
					Str("Ticker", p.ticker).
					Str("MassiveType", string(assetType)).
					Str("SECType", string(overrideType)).
					Str("CIK", cik).
					Time("RangeStart", p.rng.Start).
					Time("RangeEnd", p.rng.End).
					Msg("massive builder: SEC submissions show exclusively operating-company forms; overriding Massive's investment-vehicle type")

				assetType = overrideType
			}
		}
	}

	// Massive's per-ticker reference returns type=FUND for both
	// closed-end funds (which trade intraday on exchanges) and open-end
	// mutual funds (which only price at daily NAV close). The lifecycle
	// reaching finalize originated in the EOD archive, so by construction
	// there are exchange-traded bars in its range — that is a CEF, not an
	// open-end mutual. Promote the label before the tracked-types gate.
	// Runs after the SEC override so a FUND-typed operating-company
	// override to CS happens first; only genuine FUND-typed entities
	// (whose SEC filings have N-series forms) reach this promotion.
	if assetType == "FUND" {
		assetType = data.CEF
	}

	if _, ok := b.tracked[string(assetType)]; !ok {
		logger.Debug().
			Str("Ticker", p.ticker).
			Str("AssetType", string(assetType)).
			Str("Name", p.record.Name).
			Str("CIK", p.record.CIK).
			Time("RangeStart", p.rng.Start).
			Time("RangeEnd", p.rng.End).
			Time("ProbedAt", p.probeAt).
			Msg("massive builder: dropping lifecycle whose asset type is not tracked")

		return nil
	}

	cik := correctCIKIfMisattributed(ctx, p.ticker, p.record.Name, strings.TrimSpace(p.record.CIK), p.rng)

	composite := strings.TrimSpace(p.record.CompositeFIGI)
	shareClass := strings.TrimSpace(p.record.ShareClassFIGI)

	syntheticReason := ""

	switch {
	case composite == "":
		// Massive returned no composite_figi (the DAL historical shape
		// for pre-bankruptcy lifecycles). Mint a synthetic and clear
		// share-class so the identifier pair stays consistent.
		composite, shareClass = mintBuilderSynthetic(cik, p.ticker, p.record.Name, p.rng.Start), ""
		syntheticReason = "massive_returned_empty_composite_figi"

		logger.Info().
			Str("Ticker", p.ticker).
			Str("Name", p.record.Name).
			Str("CIK", cik).
			Str("MintedComposite", composite).
			Time("RangeStart", p.rng.Start).
			Time("RangeEnd", p.rng.End).
			Msg("massive builder: Massive returned no composite_figi; minted synthetic")
	case composite != "" && containsFIGI(nonUSFigis, composite):
		// OpenFIGI confirmed the FIGI belongs to a non-US listing
		// (AVP / BBT / GSH shape). The EOD archive has US bars by
		// construction, so the historical US lifecycle keeps a
		// synthetic FIGI and discards the foreign-tainted one.
		logger.Info().
			Str("Ticker", p.ticker).
			Str("DiscardedComposite", composite).
			Str("DiscardedShareClass", shareClass).
			Msg("massive builder: composite_figi is non-US per OpenFIGI; minting synthetic for the US lifecycle")

		composite, shareClass = mintBuilderSynthetic(cik, p.ticker, p.record.Name, p.rng.Start), ""
		syntheticReason = "openfigi_says_non_us"
	}

	if composite == "" {
		logger.Warn().
			Str("Ticker", p.ticker).
			Time("RangeStart", p.rng.Start).
			Time("RangeEnd", p.rng.End).
			Msg("massive builder: cannot produce a composite FIGI (no CIK, no name); dropping lifecycle")

		return nil
	}

	var sharadarRec *sharadarRecord
	if b.sharadar != nil {
		sharadarRec = b.sharadar.lookupByLifecycle(p.ticker, p.rng)
	}

	listed, listedSource := b.chooseListingDate(ctx, p, sharadarRec)

	var (
		delistedDate time.Time
		active       bool
	)

	// `active=true only if last range AND ticker is in today's
	// snapshot.` Any other case is a closed lifecycle: delisted is the
	// trading day after the range's last bar.
	if p.isLast && b.tickerActiveToday(p.ticker) {
		active = true
	} else {
		delistedDate = p.rng.End.AddDate(0, 0, 1)
	}

	location := ""
	if p.record.Address.City != "" {
		location = fmt.Sprintf("%s, %s", p.record.Address.City, p.record.Address.State)
	}

	sicCode := 0
	if n, err := strconv.Atoi(strings.TrimSpace(p.record.SIC)); err == nil {
		sicCode = n
	}

	delistedStr := ""
	if !delistedDate.IsZero() {
		delistedStr = delistedDate.Format("2006-01-02")
	}

	publishLog := logger.Info().
		Str("Ticker", p.ticker).
		Str("CompositeFigi", composite).
		Str("ShareClassFigi", shareClass).
		Str("Name", p.record.Name).
		Str("AssetType", string(assetType)).
		Str("CIK", cik).
		Str("Listed", listed.Format("2006-01-02")).
		Str("Delisted", delistedStr).
		Bool("Active", active).
		Str("ListedSource", listedSource).
		Time("RangeStart", p.rng.Start).
		Time("RangeEnd", p.rng.End)
	if syntheticReason != "" {
		publishLog = publishLog.Str("SyntheticReason", syntheticReason)
	}

	publishLog.Msg("massive builder: built asset row")

	asset := &data.Asset{
		Ticker:               p.ticker,
		CompositeFigi:        composite,
		ShareClassFigi:       shareClass,
		Name:                 p.record.Name,
		Description:          p.record.Description,
		Active:               active,
		PrimaryExchange:      massiveExchangeMap[p.record.PrimaryExchange],
		AssetType:            assetType,
		HeadquartersLocation: location,
		CIK:                  cik,
		SIC:                  &sicCode,
		CorporateUrl:         p.record.CorporateURL,
		ListingDate:          listed.Format("2006-01-02"),
		DelistingDate:        delistedStr,
		LastUpdated:          time.Now(),
		// ValidFor is the trading-evidence date inside the lifecycle.
		// Use the range's last day so SaveDB treats the row as the
		// authoritative observation for that lifecycle's delisted /
		// active state.
		ValidFor: p.rng.End,
	}

	if sharadarRec != nil {
		applySharadarEnrichment(ctx, asset, sharadarRec)
	}

	return asset
}

// chooseListingDate returns the listed date and a source label for
// the proposal. The default is the EOD lifecycle's Start. When that
// Start coincides with the archive's overall coverage Start (the
// left edge), we cannot tell from EOD alone whether the asset began
// trading on that day or earlier — coverage just doesn't reach
// further back. Reference sources are consulted in priority order:
//
//  1. Massive's per-ticker reference list_date, when present, non-
//     sentinel, and strictly before the EOD Start.
//  2. Sharadar's FirstPriceDate, under the same conditions plus
//     rejection of Sharadar's 1986-01-01 data-start stamp.
//
// Both candidates must precede the EOD Start; a reference date inside
// or after the bars contradicts trading evidence and is discarded.
func (b *AssetBuilder) chooseListingDate(ctx context.Context, p *proposedAsset, sharadarRec *sharadarRecord) (time.Time, string) {
	logger := zerolog.Ctx(ctx)

	coverageStart, _ := b.archive.Coverage()
	onLeftEdge := !coverageStart.IsZero() && coverageStart.Equal(p.rng.Start)

	if !onLeftEdge {
		return p.rng.Start, "eod_range_start"
	}

	massiveListDate := parseMassiveListDate(p.record.ListDate)
	if !massiveListDate.IsZero() && massiveListDate.Before(p.rng.Start) {
		logger.Info().
			Str("Ticker", p.ticker).
			Time("EODRangeStart", p.rng.Start).
			Time("CoverageStart", coverageStart).
			Time("MassiveListDate", massiveListDate).
			Msg("catalog builder: lifecycle sits on left edge of EOD coverage; using Massive list_date as listed")

		return massiveListDate, "massive_list_date_left_edge_override"
	}

	if sharadarRec != nil && sharadarListDateUsable(sharadarRec.FirstPriceDate, p.rng.Start) {
		logger.Info().
			Str("Ticker", p.ticker).
			Time("EODRangeStart", p.rng.Start).
			Time("CoverageStart", coverageStart).
			Time("SharadarFirstPriceDate", sharadarRec.FirstPriceDate).
			Msg("catalog builder: lifecycle sits on left edge of EOD coverage; Massive list_date unusable, using Sharadar first_price_date as listed")

		return sharadarRec.FirstPriceDate, "sharadar_first_price_date_left_edge_override"
	}

	return p.rng.Start, "eod_range_start"
}

// tickerActiveToday reports whether the builder's caller said this
// ticker appears in today's active snapshot. The snapshot is keyed by
// the Massive ticker (after pvTicker normalization) so the comparison
// is direct.
func (b *AssetBuilder) tickerActiveToday(ticker string) bool {
	if b.todayIDs == nil {
		return false
	}

	_, ok := b.todayIDs[ticker]

	return ok
}

// massiveListDateSentinels are values Massive returns as stand-ins for
// "list_date unknown" rather than a real first-trading day; rejecting
// them keeps the left-edge override from publishing an obviously bogus
// listed date. Verified examples:
//
//   - 1899-12-30 (Excel 1900-system epoch) — observed on BF/A despite
//     Brown-Forman's true listing being 1933.
//   - 1972-06-01 — 174 records share this date, including AAL, ADI,
//     AIR, AVY, ALK etc. that all IPO'd in different years.
var massiveListDateSentinels = map[string]struct{}{
	"1899-12-30": {},
	"1972-06-01": {},
}

// parseMassiveListDate returns the parsed Massive list_date when raw
// is a YYYY-MM-DD string that is neither empty nor a known sentinel.
// Returns the zero time.Time on any failure mode so callers can do a
// single IsZero check.
func parseMassiveListDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}

	if _, sentinel := massiveListDateSentinels[raw]; sentinel {
		return time.Time{}
	}

	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}
	}

	return t
}

// mintBuilderSynthetic mints a deterministic synthetic FIGI for the
// (cik, ticker, lifecycle-start) tuple when CIK is available, falling
// back to (ticker, name, lifecycle-start) when not. The lifecycle start
// date is part of the seed so adjacent lifecycles for the same entity
// (e.g., a relisting after a delinquency-suffix period) receive distinct
// FIGIs and don't collide on the (ticker, composite_figi) primary key.
// Returns "" only when both fallbacks are unusable — the caller is
// expected to drop the lifecycle in that case.
func mintBuilderSynthetic(cik, ticker, name string, lifecycleStart time.Time) string {
	cik = strings.TrimSpace(cik)
	ticker = strings.TrimSpace(ticker)
	name = strings.TrimSpace(name)
	start := lifecycleStart.UTC().Format("2006-01-02")

	switch {
	case cik != "":
		return figi.GenerateSyntheticFIGIFromCIKLifecycle(cik, ticker, start)
	case ticker != "" && name != "":
		return figi.GenerateSyntheticFIGILifecycle(ticker, name, start)
	default:
		return ""
	}
}

// containsFIGI is a tiny helper that treats an empty input as
// definitely-not-present without dereferencing the map. Keeps the
// finalize switch readable.
func containsFIGI(set map[string]struct{}, f string) bool {
	if f == "" {
		return false
	}

	_, ok := set[f]

	return ok
}

// cikMisattributionGraceDays is how far Massive's supplied CIK's
// earliest SEC filing may postdate the lifecycle's first EOD bar
// before we flag the CIK as misattributed. One year is well outside
// any plausible filing lag (companies file their first 10-K within
// months of going public, not years).
const cikMisattributionGraceDays = 365

// candidateClaimsTicker reports whether the candidate CIK's SEC
// tickers list contains ticker (case- and whitespace-insensitive).
// An empty tickers list returns false; the caller is expected to
// treat empty as silent rather than as a positive conflict signal.
func candidateClaimsTicker(list []string, ticker string) bool {
	target := strings.ToUpper(strings.TrimSpace(ticker))
	if target == "" {
		return false
	}

	for _, t := range list {
		if strings.ToUpper(strings.TrimSpace(t)) == target {
			return true
		}
	}

	return false
}

// correctCIKIfMisattributed detects and corrects cases where Massive
// has tagged a ticker with a CIK that belongs to a different entity.
// The signal is a time gap: the SEC-recorded earliest filing under
// the supplied CIK is more than a year after the lifecycle's first
// EOD bar. When the gap is wide enough, the function searches SEC by
// the asset's name for a candidate CIK and swaps if the candidate
// passes the same gates the walk-time ResolveAssetTypeWithCIKCorrection
// wrapper uses. Returns the input CIK unchanged in the common
// (no-correction) case.
func correctCIKIfMisattributed(ctx context.Context, ticker, name, cik string, rng dateRange) string {
	if cik == "" || strings.TrimSpace(name) == "" {
		return cik
	}

	if rng.Start.IsZero() {
		return cik
	}

	cikN, err := strconv.Atoi(cik)
	if err != nil || cikN <= 0 {
		return cik
	}

	sub, err := sec.FetchSubmissions(ctx, cikN)
	if err != nil || sub == nil {
		return cik
	}

	earliest := sub.EarliestFilingDate()
	if earliest == "" {
		return cik
	}

	earliestT, err := time.Parse("2006-01-02", earliest)
	if err != nil {
		return cik
	}

	if earliestT.Sub(rng.Start) < time.Duration(cikMisattributionGraceDays)*24*time.Hour {
		return cik
	}

	correctedCIK, ok := sec.FindCIKByName(ctx, name, rng.Start.Year())
	if !ok {
		return cik
	}

	if strings.TrimLeft(correctedCIK, "0") == strings.TrimLeft(cik, "0") {
		return cik
	}

	candidateN, err := strconv.Atoi(correctedCIK)
	if err != nil || candidateN <= 0 {
		return cik
	}

	candidateSub, err := sec.FetchSubmissions(ctx, candidateN)
	if err != nil || candidateSub == nil {
		return cik
	}

	if len(candidateSub.Tickers) > 0 && !candidateClaimsTicker(candidateSub.Tickers, ticker) {
		return cik
	}

	log.Info().
		Str("Ticker", ticker).
		Str("OldCIK", cik).
		Str("NewCIK", correctedCIK).
		Str("OldCIKName", sub.Name).
		Str("NewCIKName", candidateSub.Name).
		Time("LifecycleStart", rng.Start).
		Str("OldCIKEarliestFiling", earliest).
		Msg("massive builder: corrected misattributed CIK via SEC name search")

	return correctedCIK
}
