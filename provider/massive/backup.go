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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gosimple/slug"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
)

// unknownTickerSampleSize bounds how many unknown ticker symbols are
// emitted in the per-file summary log. The log is informational and
// large samples drown out other context; ten symbols is enough to
// notice patterns (e.g. all OTC, all foreign ADRs) without spam.
const unknownTickerSampleSize = 10

// logUnknownTickers emits a per-file summary recording how many rows
// were published, how many were dropped because their ticker is not in
// the active asset universe, and a small alphabetical sample of the
// unknown tickers. Always logs (even on a clean file) so the operator
// can audit drop rates over time.
func logUnknownTickers(logger *zerolog.Logger, dateStr, dataset string, emitted int, unknown map[string]int) {
	dropped := 0
	for _, c := range unknown {
		dropped += c
	}

	sample := make([]string, 0, len(unknown))
	for t := range unknown {
		sample = append(sample, t)
	}

	sort.Strings(sample)

	if len(sample) > unknownTickerSampleSize {
		sample = sample[:unknownTickerSampleSize]
	}

	evt := logger.Info().
		Str("dataset", dataset).
		Str("date", dateStr).
		Int("emitted_rows", emitted).
		Int("dropped_rows", dropped).
		Int("unknown_tickers", len(unknown))

	if len(sample) > 0 {
		evt = evt.Strs("sample", sample)
	}

	evt.Msg("flat-file processed")
}

// subscriptionBackupSlug returns a stable per-subscription directory
// segment used to namespace flat-file backups so multiple subscriptions
// pointing at the same parquet_backup_dir do not overwrite one another.
// The format mirrors ComputeTableNames (slug + 5-char id suffix) so a
// backup directory is easy to correlate with its subscription row.
func subscriptionBackupSlug(sub *library.Subscription) string {
	s := slug.Make(fmt.Sprintf("%s %s", sub.Name, sub.ID.String()[:5]))
	return strings.ReplaceAll(s, "-", "_")
}

// writeCorporateActionsBackup buckets items by year (extracted via
// yearOf) and writes one parquet file per year as <baseDir>/<YYYY>.parquet.
// Existing files for the same year are overwritten - corporate actions
// in the past don't change, so a fresh write always supersedes prior
// runs. Items whose yearOf returns 0 (e.g. unparseable date) are
// grouped into a 0000.parquet file so they're still preserved.
func writeCorporateActionsBackup[T any](baseDir string, items []T, yearOf func(T) int) error {
	if len(items) == 0 {
		return nil
	}

	byYear := map[int][]T{}

	for _, item := range items {
		y := yearOf(item)
		byYear[y] = append(byYear[y], item)
	}

	for y, rows := range byYear {
		destPath := filepath.Join(baseDir, fmt.Sprintf("%04d.parquet", y))
		if err := writeFlatFileBackup(destPath, rows); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}
	}

	return nil
}

// backupPathFor returns the destination path for a flat-file backup.
// The layout is <baseDir>/<YYYY>/<YYYY-MM-DD>.parquet - the source S3
// hierarchy (us_stocks_sip/<format>_aggs_v1/) is dropped because the
// per-subscription baseDir already disambiguates one dataset from
// another, and a single year (~252 trading days) fits comfortably in
// one directory.
func backupPathFor(baseDir string, d time.Time) string {
	return filepath.Join(baseDir, fmt.Sprintf("%04d", d.Year()), d.Format("2006-01-02")+".parquet")
}

// backupExists reports whether destPath already holds a backup file.
// Used so writeFlatFileBackup can skip days that have already been
// archived without re-doing the parquet encode.
func backupExists(destPath string) (bool, error) {
	_, err := os.Stat(destPath)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	return false, err
}

// writeFlatFileBackup writes rows to destPath as a ZSTD-compressed
// parquet file. The file is staged at destPath+".tmp" and renamed on
// success so a crash mid-write cannot leave a partial parquet behind.
// The schema is derived from T's struct tags by parquet-go.
func writeFlatFileBackup[T any](destPath string, rows []T) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(destPath), err)
	}

	tmp := destPath + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}

	w := parquet.NewGenericWriter[T](f, parquet.Compression(&zstd.Codec{}))

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

	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("rename %s -> %s: %w", tmp, destPath, err)
	}

	return nil
}
