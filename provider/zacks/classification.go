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
package zacks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	backblaze "github.com/kothar/go-backblaze"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/reader"
)

// zacksBucket is the B2 bucket Zacks parquet snapshots are uploaded
// to. Matches the bucketName passed to backblaze.Upload at zacks.go's
// ratings-emit path.
const zacksBucket = "zacks-rank"

// classification is the subset of ZacksRecord we need for asset
// enrichment. Loaded once per process from the most recent B2 parquet
// snapshot and held in memory.
type classification struct {
	Ticker             string
	CompositeFigi      string
	Sector             string
	Industry           string
	MonthOfFiscalYrEnd int
	Optionable         bool
	InSp500            bool
}

var (
	classificationsOnce sync.Once
	classificationsErr  error
	byFigi              map[string]*classification
	byTicker            map[string]*classification
)

// EnrichClassification fills empty descriptive fields on each asset
// from the most recent Zacks parquet snapshot in B2. Looks up by
// composite_figi (preferred) then ticker. Silent no-op when B2 is
// not configured or the snapshot cannot be loaded — Zacks-sourced
// fields are nice-to-have, not required.
//
// Fields populated (only when previously empty):
//   - asset.Sector
//   - asset.Industry
//
// Zacks only covers currently-rated tickers, so this enrichment does
// not help predecessor or long-delisted asset records.
func EnrichClassification(ctx context.Context, assets ...*data.Asset) {
	logger := zerolog.Ctx(ctx)

	classificationsOnce.Do(func() {
		byFigi, byTicker, classificationsErr = loadLatestClassifications(ctx)
		if classificationsErr != nil {
			logger.Warn().Err(classificationsErr).Msg("zacks: classification load failed; sector/industry enrichment disabled for this run")
		}
	})

	if classificationsErr != nil {
		return
	}

	for _, asset := range assets {
		var match *classification

		if asset.CompositeFigi != "" {
			match = byFigi[asset.CompositeFigi]
		}

		if match == nil && asset.Ticker != "" {
			match = byTicker[asset.Ticker]
		}

		if match == nil {
			continue
		}

		if asset.Sector == "" && match.Sector != "" {
			asset.Sector = match.Sector
		}

		if asset.Industry == "" && match.Industry != "" {
			asset.Industry = match.Industry
		}
	}
}

// loadLatestClassifications downloads the most recent Zacks parquet
// from B2 and returns lookup maps keyed by composite_figi and
// ticker. Returns an empty result + nil error when B2 credentials are
// not configured.
func loadLatestClassifications(ctx context.Context) (map[string]*classification, map[string]*classification, error) {
	keyID := viper.GetString("backblaze.application_id")
	appKey := viper.GetString("backblaze.application_key")

	if keyID == "" || appKey == "" {
		return map[string]*classification{}, map[string]*classification{}, nil
	}

	logger := zerolog.Ctx(ctx)

	logger.Info().Msg("zacks: authorizing with backblaze b2")

	b2, err := backblaze.NewB2(backblaze.Credentials{KeyID: keyID, ApplicationKey: appKey})
	if err != nil {
		return nil, nil, fmt.Errorf("zacks: authorize b2: %w", err)
	}

	logger.Info().Str("Bucket", zacksBucket).Msg("zacks: looking up bucket")

	bucket, err := b2.Bucket(zacksBucket)
	if err != nil {
		return nil, nil, fmt.Errorf("zacks: lookup bucket %s: %w", zacksBucket, err)
	}

	if bucket == nil {
		return nil, nil, fmt.Errorf("zacks: bucket %s not found", zacksBucket)
	}

	logger.Info().Msg("zacks: listing files in bucket to find latest parquet")

	fileName, fileID, err := findLatestZacksFile(bucket)
	if err != nil {
		return nil, nil, err
	}

	logger.Info().Str("File", fileName).Msg("zacks: downloading latest classification parquet")

	dlStart := time.Now()

	tmpFile, err := downloadToTempFile(b2, fileID)
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(tmpFile)

	logger.Info().Str("File", fileName).Dur("Elapsed", time.Since(dlStart).Round(time.Second)).Msg("zacks: download complete; parsing parquet")

	parseStart := time.Now()

	figiIdx, tickerIdx, err := parseClassificationParquet(tmpFile)
	if err != nil {
		return nil, nil, err
	}

	logger.Info().
		Int("ClassificationsByFigi", len(figiIdx)).
		Int("ClassificationsByTicker", len(tickerIdx)).
		Str("File", fileName).
		Dur("ParseElapsed", time.Since(parseStart).Round(time.Second)).
		Msg("zacks: classification snapshot loaded")

	return figiIdx, tickerIdx, nil
}

// findLatestZacksFile lists files in zacksBucket and returns the
// name + B2 file ID of the lexicographically-largest .parquet file.
// Parquet filenames embed the date in YYYY-MM-DD form, so the
// lexicographic max matches chronological ordering.
func findLatestZacksFile(bucket *backblaze.Bucket) (string, string, error) {
	var (
		latestName string
		latestID   string
	)

	startName := ""
	for {
		resp, err := bucket.ListFileNamesWithPrefix(startName, 1000, "", "")
		if err != nil {
			return "", "", fmt.Errorf("zacks: list files: %w", err)
		}

		for _, f := range resp.Files {
			if !strings.HasSuffix(f.Name, ".parquet") {
				continue
			}

			if f.Name > latestName {
				latestName = f.Name
				latestID = f.ID
			}
		}

		if resp.NextFileName == "" {
			break
		}

		startName = resp.NextFileName
	}

	if latestName == "" {
		return "", "", errors.New("zacks: no .parquet file found in B2 bucket")
	}

	return latestName, latestID, nil
}

// downloadToTempFile fetches the file with the given B2 ID and writes
// it to a tempfile. Returns the temp path; caller is responsible for
// os.Remove.
func downloadToTempFile(b2 *backblaze.B2, fileID string) (string, error) {
	_, body, err := b2.DownloadFileByID(fileID)
	if err != nil {
		return "", fmt.Errorf("zacks: download %s: %w", fileID, err)
	}
	defer body.Close()

	tmp, err := os.CreateTemp("", "zacks-classification-*.parquet")
	if err != nil {
		return "", fmt.Errorf("zacks: create temp file: %w", err)
	}

	if _, err := io.Copy(tmp, body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("zacks: write temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("zacks: close temp file: %w", err)
	}

	return tmp.Name(), nil
}

// parseClassificationParquet reads a Zacks parquet into the two lookup
// maps. Streams in batches to avoid loading the entire ~10-20 MB file
// into one slice.
func parseClassificationParquet(path string) (map[string]*classification, map[string]*classification, error) {
	fr, err := local.NewLocalFileReader(path)
	if err != nil {
		return nil, nil, fmt.Errorf("zacks: open parquet %s: %w", path, err)
	}
	defer fr.Close()

	pr, err := reader.NewParquetReader(fr, new(ZacksRecord), 4)
	if err != nil {
		return nil, nil, fmt.Errorf("zacks: parquet reader for %s: %w", path, err)
	}
	defer pr.ReadStop()

	figiIdx := make(map[string]*classification, pr.GetNumRows())
	tickerIdx := make(map[string]*classification, pr.GetNumRows())

	const batchSize = 5000

	remaining := int(pr.GetNumRows())
	for remaining > 0 {
		n := min(batchSize, remaining)

		batch := make([]ZacksRecord, n)
		if err := pr.Read(&batch); err != nil {
			return nil, nil, fmt.Errorf("zacks: parquet read batch: %w", err)
		}

		for i := range batch {
			r := &batch[i]
			c := &classification{
				Ticker:             strings.TrimSpace(r.Ticker),
				CompositeFigi:      strings.TrimSpace(r.CompositeFigi),
				Sector:             strings.TrimSpace(r.Sector),
				Industry:           strings.TrimSpace(r.Industry),
				MonthOfFiscalYrEnd: r.MonthOfFiscalYrEnd,
				Optionable:         r.Optionable,
				InSp500:            r.InSp500,
			}

			if c.CompositeFigi != "" {
				if existing, ok := figiIdx[c.CompositeFigi]; !ok || (c.Sector != "" && existing.Sector == "") {
					figiIdx[c.CompositeFigi] = c
				}
			}

			if c.Ticker != "" {
				if existing, ok := tickerIdx[c.Ticker]; !ok || (c.Sector != "" && existing.Sector == "") {
					tickerIdx[c.Ticker] = c
				}
			}
		}

		remaining -= n
	}

	return figiIdx, tickerIdx, nil
}
