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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

// d parses a YYYY-MM-DD string into a UTC time.Time; tests panic on
// malformed input so a typo surfaces immediately rather than producing
// a silent zero value.
func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}

	return t
}

// buildBBIArchive returns an EODArchive carrying Blockbuster's BBI
// lifecycle range [2003-09-10 .. 2010-07-06]. Used by the
// shortDelistedNoCoverageReason tests as a representative archive
// covering one ticker with one closed lifecycle.
func buildBBIArchive() *EODArchive {
	archive := newEmptyArchive()
	archive.tickers["BBI"] = []dateRange{
		{Start: d("2003-09-10"), End: d("2010-07-06")},
	}
	archive.coverageStart = d("2003-09-10")
	archive.coverageEnd = d("2010-07-06")

	return archive
}

// newFetcherWithArchive returns a massiveAssetFetcher with the given
// archive pre-installed and the lazy-init sync.Once already fired so
// eodArchiveForRun returns the supplied archive without trying to
// load anything from disk.
func newFetcherWithArchive(archive *EODArchive) *massiveAssetFetcher {
	api := &massiveAssetFetcher{
		eodArchive: archive,
	}

	api.eodArchiveOnce.Do(func() {})

	return api
}

// writeFlatFileBackup writes rows to a zstd-compressed parquet file at
// destPath, mirroring the writer that produces the per-day archive
// files the EODArchive loader reads. Used by the archive-walker tests
// to set up synthetic on-disk archives.
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
