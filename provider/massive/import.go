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
package massive

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
)

// backupFilenameRE matches YYYY-MM-DD.parquet file basenames written by
// writeFlatFileBackup. The expected layout for a per-day backup is
// <base>/<YYYY>/<YYYY-MM-DD>.parquet so derivations of <base> walk two
// directories up from the file path.
var backupFilenameRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})\.parquet$`)

// ImportFiles implements provider.FileImporter for the Massive
// provider. It re-imports the per-day parquet backups produced when
// parquet_backup_dir is set on a live run, emitting Observations
// identical to the live S3 flat-file pipeline. Supported datasets are
// "EOD" (joins each row against splits/dividends loaded from the
// colocated <base>/splits/<YYYY>.parquet and <base>/dividends/
// <YYYY>.parquet files) and "1-Minute Bars". Other datasets fail fast.
//
// Arguments may be either individual daily parquet files or directories
// containing the standard <base>/<YYYY>/<YYYY-MM-DD>.parquet layout;
// directories are walked recursively and the corporate-actions
// subdirectories are ignored.
func (massive *Massive) ImportFiles(ctx context.Context, sub *library.Subscription,
	paths []string, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs

		if runSummary.Status != data.RunFailed {
			runSummary.Status = data.RunSuccess
		}

		exit <- runSummary
	}()

	files, err := expandImportPaths(paths)
	if err != nil {
		logger.Error().Err(err).Msg("could not expand import paths")

		runSummary.Status = data.RunFailed

		return
	}

	if len(files) == 0 {
		logger.Warn().Msg("no daily parquet backups found in supplied paths; nothing to import")
		return
	}

	universe, err := loadImportUniverse(ctx, sub)
	if err != nil {
		logger.Error().Err(err).Msg("could not build asset universe for import")

		runSummary.Status = data.RunFailed

		return
	}

	if universe.tickerCount() == 0 {
		logger.Warn().Msg("no assets in scope for massive import; skipping run")
		return
	}

	logger.Info().Int("files", len(files)).Str("dataset", sub.Dataset).Msg("starting massive file import")

	switch sub.Dataset {
	case "EOD":
		n, err := importEODFiles(ctx, sub, universe, files, out)
		numObs += n

		if err != nil {
			runSummary.Status = data.RunFailed
			return
		}
	case "1-Minute Bars":
		n, err := importMinuteFiles(ctx, sub, universe, files, out)
		numObs += n

		if err != nil {
			runSummary.Status = data.RunFailed
			return
		}
	default:
		logger.Error().Str("dataset", sub.Dataset).Msg("file import not supported for this Massive dataset")

		runSummary.Status = data.RunFailed
	}
}

// expandImportPaths resolves each input path to the set of daily
// aggregate parquet files it contributes. Regular files are returned
// as-is when their basename matches YYYY-MM-DD.parquet; directories
// are walked recursively for matching files, skipping the splits/ and
// dividends/ subdirectories (which hold corporate actions, not bars)
// and ignoring partial *.parquet.tmp writes. The result is sorted by
// date so processing is deterministic.
func expandImportPaths(paths []string) ([]string, error) {
	var out []string

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}

		if !info.IsDir() {
			if backupFilenameRE.MatchString(filepath.Base(p)) {
				out = append(out, p)
			}

			continue
		}

		walkErr := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if d.IsDir() {
				name := d.Name()
				if path != p && (name == "splits" || name == "dividends") {
					return fs.SkipDir
				}

				return nil
			}

			if backupFilenameRE.MatchString(d.Name()) {
				out = append(out, path)
			}

			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk %s: %w", p, walkErr)
		}
	}

	sort.Strings(out)

	return out, nil
}

// loadImportUniverse mirrors downloadMassiveEOD's universe construction
// so imported rows resolve FIGI exactly as live-fetched rows would.
func loadImportUniverse(ctx context.Context, sub *library.Subscription) (*historicalAssetUniverse, error) {
	conn, err := sub.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not acquire database connection: %w", err)
	}

	dbAssets, err := data.AllAssets(ctx, conn)

	conn.Release()

	if err != nil {
		return nil, fmt.Errorf("could not load assets: %w", err)
	}

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)

	return buildHistoricalUniverse(dbAssets, tickerFilter, figiFilter), nil
}

// importEODFiles iterates day-aggregate parquet files, joining each
// row against splits and dividends loaded lazily from the colocated
// corporate-actions backups. Returns the number of Observations
// emitted and the first fatal error.
func importEODFiles(ctx context.Context, sub *library.Subscription, universe *historicalAssetUniverse, files []string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)
	cache := newCorporateActionsCache()

	total := 0

	for _, file := range files {
		d, err := parseBackupDate(file)
		if err != nil {
			return total, fmt.Errorf("%s: %w", file, err)
		}

		baseDir, err := corporateActionsBaseDir(file)
		if err != nil {
			return total, fmt.Errorf("%s: %w", file, err)
		}

		splits, divs, err := cache.get(baseDir, d.Year())
		if err != nil {
			return total, fmt.Errorf("%s: %w", file, err)
		}

		rows, err := parquet.ReadFile[aggRow](file)
		if err != nil {
			return total, fmt.Errorf("read %s: %w", file, err)
		}

		n, err := emitEODRows(ctx, sub, universe, splits, divs, d, rows, out)
		total += n

		if err != nil {
			return total, err
		}

		logger.Info().Str("file", file).Time("date", d).Int("observations", n).Msg("imported day aggs")
	}

	return total, nil
}

// importMinuteFiles iterates minute-aggregate parquet files. Minute
// imports never need corporate actions because IntradayBars carry raw
// OHLCV; adjustments are applied at query time against the EOD splits
// table.
func importMinuteFiles(ctx context.Context, sub *library.Subscription, universe *historicalAssetUniverse, files []string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)
	total := 0

	for _, file := range files {
		d, err := parseBackupDate(file)
		if err != nil {
			return total, fmt.Errorf("%s: %w", file, err)
		}

		rows, err := parquet.ReadFile[aggRow](file)
		if err != nil {
			return total, fmt.Errorf("read %s: %w", file, err)
		}

		n, err := emitMinuteRows(ctx, sub, universe, d, rows, out)
		total += n

		if err != nil {
			return total, err
		}

		logger.Info().Str("file", file).Time("date", d).Int("observations", n).Msg("imported minute aggs")
	}

	return total, nil
}

// emitEODRows mirrors streamDayAggsForDate's per-row transformation
// but reads from in-memory rows and uses the date parsed from the
// backup filename rather than the live S3 key.
func emitEODRows(ctx context.Context, sub *library.Subscription, universe *historicalAssetUniverse, splits, divs corporateActions, d time.Time, rows []aggRow, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	eventTime, err := buildMassiveEodEvent(d)
	if err != nil {
		return 0, err
	}

	dateStr := d.Format("2006-01-02")
	n := 0
	unknown := map[string]int{}

	for _, row := range rows {
		ticker := massiveTicker2PvTicker(row.Ticker)

		figi, ok := universe.figiAt(ticker, d)
		if !ok {
			unknown[ticker]++
			continue
		}

		split := splits.lookup(ticker, dateStr)
		if split == 0 {
			split = 1.0
		}

		eod := &data.Eod{
			Date:          eventTime,
			Ticker:        ticker,
			CompositeFigi: figi,
			Open:          row.Open,
			High:          row.High,
			Low:           row.Low,
			Close:         row.Close,
			Volume:        row.Volume,
			Dividend:      divs.lookup(ticker, dateStr),
			Split:         split,
		}

		select {
		case <-ctx.Done():
			return n, ctx.Err()
		case out <- &data.Observation{
			EodQuote:         eod,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}:
			n++
		}
	}

	logUnknownTickers(logger, dateStr, "day_aggs", n, unknown)

	return n, nil
}

// emitMinuteRows mirrors streamMinuteAggsForDate's per-row
// transformation, sourcing rows from a parquet backup.
func emitMinuteRows(ctx context.Context, sub *library.Subscription, universe *historicalAssetUniverse, d time.Time, rows []aggRow, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	n := 0
	unknown := map[string]int{}

	for _, row := range rows {
		ticker := massiveTicker2PvTicker(row.Ticker)

		figi, ok := universe.figiAt(ticker, d)
		if !ok {
			unknown[ticker]++
			continue
		}

		bar := &data.IntradayBar{
			Date:          time.Unix(0, row.WindowStart).UTC(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Open:          row.Open,
			High:          row.High,
			Low:           row.Low,
			Close:         row.Close,
			Volume:        row.Volume,
		}

		select {
		case <-ctx.Done():
			return n, ctx.Err()
		case out <- &data.Observation{
			IntradayBar:      bar,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}:
			n++
		}
	}

	logUnknownTickers(logger, d.Format("2006-01-02"), "minute_aggs", n, unknown)

	return n, nil
}

// parseBackupDate extracts the trading date from a per-day backup
// path. Paths must end in <YYYY-MM-DD>.parquet (the layout written by
// writeFlatFileBackup); other names are rejected so we never silently
// import a parquet whose date we cannot determine.
func parseBackupDate(path string) (time.Time, error) {
	base := filepath.Base(path)

	m := backupFilenameRE.FindStringSubmatch(base)
	if m == nil {
		return time.Time{}, fmt.Errorf("filename %q is not in YYYY-MM-DD.parquet form", base)
	}

	d, err := time.Parse("2006-01-02", fmt.Sprintf("%s-%s-%s", m[1], m[2], m[3]))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date in filename %q: %w", base, err)
	}

	return d, nil
}

// corporateActionsBaseDir returns the directory that should contain
// splits/ and dividends/ subdirectories. The backup layout is
// <base>/<YYYY>/<YYYY-MM-DD>.parquet so <base> is the file's
// grandparent.
func corporateActionsBaseDir(filePath string) (string, error) {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}

	parent := filepath.Dir(abs)
	grandparent := filepath.Dir(parent)

	if parent == grandparent || parent == "." || parent == string(filepath.Separator) {
		return "", fmt.Errorf("path %q is not nested under <base>/<YYYY>/", filePath)
	}

	return grandparent, nil
}

// corporateActionsCache lazily loads and memoises splits and
// dividends parquet files keyed by (baseDir, year). Each key is read
// once even when many daily aggregate files reference it.
type corporateActionsCache struct {
	splits map[string]corporateActions
	divs   map[string]corporateActions
}

func newCorporateActionsCache() *corporateActionsCache {
	return &corporateActionsCache{
		splits: map[string]corporateActions{},
		divs:   map[string]corporateActions{},
	}
}

func (c *corporateActionsCache) get(baseDir string, year int) (corporateActions, corporateActions, error) {
	key := fmt.Sprintf("%s|%04d", baseDir, year)

	splits, ok := c.splits[key]
	if !ok {
		loaded, err := loadSplitsForYear(baseDir, year)
		if err != nil {
			return nil, nil, err
		}

		c.splits[key] = loaded
		splits = loaded
	}

	divs, ok := c.divs[key]
	if !ok {
		loaded, err := loadDividendsForYear(baseDir, year)
		if err != nil {
			return nil, nil, err
		}

		c.divs[key] = loaded
		divs = loaded
	}

	return splits, divs, nil
}

// loadSplitsForYear reads <baseDir>/splits/<YYYY>.parquet and projects
// it into the corporateActions lookup map. A missing file is a fatal
// error: importing EOD without the splits backup would silently zero
// out the split factor on previously-correct rows during the upsert.
func loadSplitsForYear(baseDir string, year int) (corporateActions, error) {
	path := filepath.Join(baseDir, "splits", fmt.Sprintf("%04d.parquet", year))

	rows, err := parquet.ReadFile[massiveSplit](path)
	if err != nil {
		return nil, fmt.Errorf("read splits %s: %w", path, err)
	}

	out := newCorporateActions(len(rows))

	for _, r := range rows {
		if r.SplitFrom == 0 {
			continue
		}

		factor := r.SplitTo / r.SplitFrom
		out.set(massiveTicker2PvTicker(r.Ticker), r.ExecutionDate, factor)
	}

	return out, nil
}

// loadDividendsForYear reads <baseDir>/dividends/<YYYY>.parquet and
// projects it into the corporateActions lookup map. Same rationale as
// loadSplitsForYear for treating a missing file as fatal.
func loadDividendsForYear(baseDir string, year int) (corporateActions, error) {
	path := filepath.Join(baseDir, "dividends", fmt.Sprintf("%04d.parquet", year))

	rows, err := parquet.ReadFile[massiveDividend](path)
	if err != nil {
		return nil, fmt.Errorf("read dividends %s: %w", path, err)
	}

	out := newCorporateActions(len(rows))

	for _, r := range rows {
		if r.CashAmount == 0 {
			continue
		}

		out.set(massiveTicker2PvTicker(r.Ticker), r.ExDividendDate, r.CashAmount)
	}

	return out, nil
}
