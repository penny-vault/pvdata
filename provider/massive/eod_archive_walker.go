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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// archiveIndexFilename is the name of the parquet sidecar that lives
// in the archive root alongside the per-year parquet subdirectories.
// It caches the per-ticker date ranges so a re-run does not have to
// re-parse every per-day parquet from scratch.
const archiveIndexFilename = "_pvdata_eod_index.parquet"

// archiveIndexMetaKey* are the keys we attach to the parquet file's
// key-value metadata so file-level facts (schema version, coverage
// bounds, last-indexed date) ride alongside the per-range rows.
const (
	archiveIndexMetaSchemaVersion   = "pvdata.eod_index.schema_version"
	archiveIndexMetaLastIndexedDate = "pvdata.eod_index.last_indexed_date"
	archiveIndexMetaCoverageStart   = "pvdata.eod_index.coverage_start"
	archiveIndexMetaCoverageEnd     = "pvdata.eod_index.coverage_end"
)

// archiveIndexSchemaVersion is bumped whenever the on-disk index
// format changes incompatibly. A load that sees an unknown version
// falls back to a full rescan.
const archiveIndexSchemaVersion = 1

// archiveLifecycleGapDays splits a ticker's observed-date history
// into separate lifecycle ranges whenever consecutive observations
// are more than this many calendar days apart. A gap that wide is
// strong evidence of a delisting followed by a same-ticker reissue
// (see Alcoa-style cases) rather than a normal weekend / holiday.
const archiveLifecycleGapDays = 20

// archiveScanProgressInterval is how many per-day parquet files we
// process between progress log lines while scanning the archive. A
// modest cadence keeps a multi-thousand-file rescan visible without
// drowning the log.
const archiveScanProgressInterval = 250

// EODArchive is a read-only index over the daily-aggregate parquet
// backups that the EOD walker writes under `parquet_backup_dir`. For
// every ticker it records one or more (start, end) date ranges; for
// the archive as a whole it records the earliest and latest dates
// observed across every file scanned. The ranges feed the
// MassiveEODArchive fields on DateCandidates so the date-assignment
// algorithm can use real trading observations as one of its sources.
type EODArchive struct {
	tickers       map[string][]dateRange
	coverageStart time.Time
	coverageEnd   time.Time

	// lastIndexedDate is the latest per-day file date already merged
	// into tickers. Persisted alongside the on-disk index so a re-run
	// only needs to read files newer than this date.
	lastIndexedDate time.Time
}

// dateRange is a contiguous lifecycle span [Start, End] for one
// ticker. A ticker reused after a gap greater than
// archiveLifecycleGapDays produces two ranges (predecessor and
// successor) rather than one merged span.
type dateRange struct {
	Start time.Time
	End   time.Time
}

// formatDateRanges renders a slice of dateRange values as a compact
// "[start..end,start..end]" string for log lines that want to show
// every lifecycle the archive knows for a ticker in one field. Dates
// are printed in YYYY-MM-DD form; an empty slice renders as "[]".
func formatDateRanges(ranges []dateRange) string {
	if len(ranges) == 0 {
		return "[]"
	}

	parts := make([]string, len(ranges))
	for i, r := range ranges {
		parts[i] = r.Start.Format("2006-01-02") + ".." + r.End.Format("2006-01-02")
	}

	return "[" + strings.Join(parts, ",") + "]"
}

// archiveIndexRow is the on-disk row shape of the parquet sidecar
// index. One row per (ticker, lifecycle range). File-level facts
// (schema version, coverage bounds, last indexed date) live in the
// parquet KeyValueMetadata.
type archiveIndexRow struct {
	Ticker string `parquet:"ticker"`
	Start  string `parquet:"start"`
	End    string `parquet:"end"`
}

// archiveDateFile matches the per-day parquet filenames produced by
// writeFlatFileBackup / backupPathFor. Captured group 1 is the
// YYYY-MM-DD date used as that file's observation date.
var archiveDateFile = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\.parquet$`)

// LoadEODArchive returns an EODArchive populated from the on-disk
// JSON index when present, then incrementally extended with any
// per-day parquet files whose date is newer than the index's
// lastIndexedDate. A missing rootDir or absent index just produces
// the empty starting state; the algorithm then reads every per-day
// parquet from scratch and writes a fresh index at the end. An
// unreadable parquet file aborts the load so a corruption issue
// does not silently drop observations.
func LoadEODArchive(rootDir string) (*EODArchive, error) {
	archive := newEmptyArchive()

	if rootDir == "" {
		return archive, nil
	}

	info, err := os.Stat(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return archive, nil
		}

		return nil, fmt.Errorf("stat eod archive root %s: %w", rootDir, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("eod archive root %s is not a directory", rootDir)
	}

	indexPath := filepath.Join(rootDir, archiveIndexFilename)

	if err := archive.loadIndex(indexPath); err != nil {
		// A bad index is recoverable: log it, drop back to the empty
		// state, and let the walk repopulate from scratch.
		log.Warn().Err(err).Str("Index", indexPath).Msg("massive: eod index unreadable; rebuilding from parquet files")

		archive = newEmptyArchive()
	}

	loadStart := time.Now()

	if archive.lastIndexedDate.IsZero() {
		log.Info().Str("Root", rootDir).Msg("massive: building eod index from scratch")
	} else {
		log.Info().
			Str("Root", rootDir).
			Time("ResumeAfter", archive.lastIndexedDate).
			Int("Tickers", len(archive.tickers)).
			Msg("massive: extending eod index from cached state")
	}

	filesProcessed := 0
	resumeAfter := archive.lastIndexedDate
	sampled := log.Logger.Sample(&zerolog.BasicSampler{N: archiveScanProgressInterval})

	err = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		match := archiveDateFile.FindStringSubmatch(filepath.Base(path))
		if match == nil {
			return nil
		}

		date, err := time.Parse("2006-01-02", match[1])
		if err != nil {
			return nil
		}

		if !resumeAfter.IsZero() && !date.After(resumeAfter) {
			return nil
		}

		if err := archive.absorbFile(path, date); err != nil {
			return fmt.Errorf("absorb eod archive file %s: %w", path, err)
		}

		filesProcessed++

		sampled.Info().
			Int("FilesScanned", filesProcessed).
			Str("LastFile", filepath.Base(path)).
			Int("Tickers", len(archive.tickers)).
			Dur("Elapsed", time.Since(loadStart).Round(time.Second)).
			Msg("massive: eod archive scan in progress")

		return nil
	})
	if err != nil {
		return nil, err
	}

	log.Info().
		Str("Root", rootDir).
		Int("FilesScanned", filesProcessed).
		Int("Tickers", len(archive.tickers)).
		Dur("Elapsed", time.Since(loadStart).Round(time.Second)).
		Msg("massive: eod archive scan complete")

	if filesProcessed > 0 {
		writeStart := time.Now()

		if err := archive.saveIndex(indexPath); err != nil {
			log.Warn().Err(err).Str("Index", indexPath).Msg("massive: failed to write eod index sidecar; next run will rescan from scratch")
		} else {
			rangeCount := 0
			for _, ranges := range archive.tickers {
				rangeCount += len(ranges)
			}

			log.Info().
				Str("Index", indexPath).
				Int("Tickers", len(archive.tickers)).
				Int("Ranges", rangeCount).
				Dur("Elapsed", time.Since(writeStart).Round(time.Second)).
				Msg("massive: eod index sidecar written")
		}
	}

	return archive, nil
}

// newEmptyArchive returns a fresh, empty EODArchive.
func newEmptyArchive() *EODArchive {
	return &EODArchive{tickers: map[string][]dateRange{}}
}

// loadIndex reads the parquet sidecar at indexPath into the receiver.
// A missing file is not an error — the receiver is left empty so the
// caller can do a full scan. An unsupported schema-version stamp is
// returned as an error so the caller can fall back to rebuilding.
func (a *EODArchive) loadIndex(indexPath string) error {
	f, err := os.Open(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("open eod index %s: %w", indexPath, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat eod index %s: %w", indexPath, err)
	}

	pf, err := parquet.OpenFile(f, stat.Size())
	if err != nil {
		return fmt.Errorf("open parquet eod index %s: %w", indexPath, err)
	}

	versionStr, ok := pf.Lookup(archiveIndexMetaSchemaVersion)
	if !ok {
		return fmt.Errorf("eod index %s missing %q metadata", indexPath, archiveIndexMetaSchemaVersion)
	}

	version, err := strconv.Atoi(versionStr)
	if err != nil {
		return fmt.Errorf("eod index %s has malformed schema-version %q", indexPath, versionStr)
	}

	if version != archiveIndexSchemaVersion {
		return fmt.Errorf("eod index %s has unsupported schema version %d (this build expects %d)", indexPath, version, archiveIndexSchemaVersion)
	}

	if v, ok := pf.Lookup(archiveIndexMetaLastIndexedDate); ok {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			a.lastIndexedDate = t
		}
	}

	if v, ok := pf.Lookup(archiveIndexMetaCoverageStart); ok {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			a.coverageStart = t
		}
	}

	if v, ok := pf.Lookup(archiveIndexMetaCoverageEnd); ok {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			a.coverageEnd = t
		}
	}

	rows, err := parquet.ReadFile[archiveIndexRow](indexPath)
	if err != nil {
		return fmt.Errorf("read parquet eod index rows %s: %w", indexPath, err)
	}

	for i := range rows {
		start, errStart := time.Parse("2006-01-02", rows[i].Start)
		end, errEnd := time.Parse("2006-01-02", rows[i].End)

		if errStart != nil || errEnd != nil {
			continue
		}

		a.tickers[rows[i].Ticker] = append(a.tickers[rows[i].Ticker], dateRange{Start: start, End: end})
	}

	return nil
}

// saveIndex serializes the receiver to indexPath atomically (write to
// .tmp, rename). A partial write left behind by a crash will never be
// read because the rename is the commit point.
func (a *EODArchive) saveIndex(indexPath string) error {
	tickers := make([]string, 0, len(a.tickers))
	for t := range a.tickers {
		tickers = append(tickers, t)
	}

	sort.Strings(tickers)

	rows := make([]archiveIndexRow, 0, len(a.tickers))

	for _, ticker := range tickers {
		for _, r := range a.tickers[ticker] {
			rows = append(rows, archiveIndexRow{
				Ticker: ticker,
				Start:  r.Start.Format("2006-01-02"),
				End:    r.End.Format("2006-01-02"),
			})
		}
	}

	tmp := indexPath + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}

	w := parquet.NewGenericWriter[archiveIndexRow](f,
		parquet.Compression(&zstd.Codec{}),
		parquet.KeyValueMetadata(archiveIndexMetaSchemaVersion, strconv.Itoa(archiveIndexSchemaVersion)),
		parquet.KeyValueMetadata(archiveIndexMetaLastIndexedDate, a.lastIndexedDate.Format("2006-01-02")),
		parquet.KeyValueMetadata(archiveIndexMetaCoverageStart, a.coverageStart.Format("2006-01-02")),
		parquet.KeyValueMetadata(archiveIndexMetaCoverageEnd, a.coverageEnd.Format("2006-01-02")),
	)

	if _, err := w.Write(rows); err != nil {
		cleanup()

		return fmt.Errorf("write parquet rows to %s: %w", tmp, err)
	}

	if err := w.Close(); err != nil {
		cleanup()

		return fmt.Errorf("close parquet writer for %s: %w", tmp, err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("close %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, indexPath); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("rename %s -> %s: %w", tmp, indexPath, err)
	}

	return nil
}

// absorbFile reads one per-day parquet file, extends each ticker's
// range list with the file's observation date, and pushes the
// archive-wide coverage bounds out to include the date.
func (a *EODArchive) absorbFile(path string, date time.Time) error {
	rows, err := parquet.ReadFile[aggRow](path)
	if err != nil {
		return err
	}

	if a.coverageStart.IsZero() || date.Before(a.coverageStart) {
		a.coverageStart = date
	}

	if a.coverageEnd.IsZero() || date.After(a.coverageEnd) {
		a.coverageEnd = date
	}

	gap := time.Duration(archiveLifecycleGapDays) * 24 * time.Hour

	for i := range rows {
		ticker := strings.TrimSpace(rows[i].Ticker)
		if ticker == "" {
			continue
		}

		// Normalize Massive's class-share separator (e.g. "BF.A") to the
		// rest of pvdata's convention ("BF/A") so lookups by asset
		// ticker hit the index regardless of which side wrote the
		// archive.
		ticker = strings.ReplaceAll(ticker, ".", "/")

		ranges := a.tickers[ticker]

		switch {
		case len(ranges) == 0:
			ranges = []dateRange{{Start: date, End: date}}
		case date.Sub(ranges[len(ranges)-1].End) > gap:
			ranges = append(ranges, dateRange{Start: date, End: date})
		case date.After(ranges[len(ranges)-1].End):
			ranges[len(ranges)-1].End = date
		}

		a.tickers[ticker] = ranges
	}

	if date.After(a.lastIndexedDate) {
		a.lastIndexedDate = date
	}

	return nil
}

// Coverage returns the earliest and latest observation dates across
// every file the archive absorbed. Both are zero values when the
// archive is empty.
func (a *EODArchive) Coverage() (start, end time.Time) {
	if a == nil {
		return time.Time{}, time.Time{}
	}

	return a.coverageStart, a.coverageEnd
}

// TickerCount returns the number of distinct tickers the archive has
// at least one bar for. Used by the loader's info-level summary log.
func (a *EODArchive) TickerCount() int {
	if a == nil {
		return 0
	}

	return len(a.tickers)
}

// Lookup returns the earliest and latest bar dates the archive has
// for ticker across all of its lifecycle ranges, plus ok=true when
// any bar was found. Callers that need per-lifecycle bounds (for the
// Alcoa-style ticker-reuse case) should reach for Ranges instead.
func (a *EODArchive) Lookup(ticker string) (first, last time.Time, ok bool) {
	if a == nil {
		return time.Time{}, time.Time{}, false
	}

	ranges, found := a.tickers[ticker]
	if !found || len(ranges) == 0 {
		return time.Time{}, time.Time{}, false
	}

	first = ranges[0].Start
	last = ranges[0].End

	for _, r := range ranges[1:] {
		if r.Start.Before(first) {
			first = r.Start
		}

		if r.End.After(last) {
			last = r.End
		}
	}

	return first, last, true
}

// Ranges returns the per-lifecycle (Start, End) spans for ticker,
// sorted by Start ascending. An empty slice means the archive has no
// bars for ticker.
func (a *EODArchive) Ranges(ticker string) []dateRange {
	if a == nil {
		return nil
	}

	src := a.tickers[ticker]
	if len(src) == 0 {
		return nil
	}

	out := make([]dateRange, len(src))
	copy(out, src)

	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })

	return out
}

// eodArchiveMu / eodArchiveCache memoize the loaded archive keyed by
// rootDir so multiple fetchers that share a parquet_backup_dir do
// not each pay the load cost. Locking is intentionally simple: a
// single sync.Mutex guarding the map is sufficient because loads are
// rare (once per process) and the map only grows.
var (
	eodArchiveMu    sync.Mutex
	eodArchiveCache = map[string]*EODArchive{}
)

// loadOrReuseEODArchive returns the cached *EODArchive for rootDir,
// loading it on first use. Errors from the underlying LoadEODArchive
// are returned to the caller; failed loads are NOT cached so a
// transient I/O failure does not stick for the rest of the process.
func loadOrReuseEODArchive(rootDir string) (*EODArchive, error) {
	eodArchiveMu.Lock()
	defer eodArchiveMu.Unlock()

	if existing, ok := eodArchiveCache[rootDir]; ok {
		return existing, nil
	}

	archive, err := LoadEODArchive(rootDir)
	if err != nil {
		return nil, err
	}

	eodArchiveCache[rootDir] = archive

	return archive, nil
}
