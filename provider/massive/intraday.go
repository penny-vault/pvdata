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
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

const (
	minuteAggsKeyFormat = "us_stocks_sip/minute_aggs_v1/%04d/%02d/%s.csv.gz"

	// defaultMinuteLookback is the window we cover when no explicit
	// lookback is set on the run context. A week is enough to recover
	// from a few missed days without pulling unbounded history; minute
	// files are ~28 MB gzipped and ~1.9M rows each so longer windows
	// should be opted in deliberately.
	defaultMinuteLookback = 7 * 24 * time.Hour
)

// minuteAggsKey renders the S3 object key for the 1-minute aggregates
// flat file covering the given UTC date.
func minuteAggsKey(d time.Time) string {
	return fmt.Sprintf(minuteAggsKeyFormat, d.Year(), int(d.Month()), d.Format("2006-01-02"))
}

func downloadMassiveMinute(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
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

	accessKey := strings.TrimSpace(sub.Config["flatFilesAccessKey"])
	secretKey := strings.TrimSpace(sub.Config["flatFilesSecretKey"])

	if accessKey == "" || secretKey == "" {
		logger.Error().Msg("massive 1-minute requires flatFilesAccessKey and flatFilesSecretKey")

		runSummary.Status = data.RunFailed

		return
	}

	s3Client := newFlatFilesClient(accessKey, secretKey)

	end := time.Now().UTC().Truncate(24 * time.Hour)
	lookback := provider.LookbackFromContext(ctx, defaultMinuteLookback)
	start := end.Add(-lookback).Truncate(24 * time.Hour)

	conn, err := sub.Library.AcquireWithTimeout(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("could not acquire database connection")

		runSummary.Status = data.RunFailed

		return
	}

	// Asset universe always reads from the published "assets" view -
	// it is the canonical union across every asset-producing
	// subscription. See provider/massive/eod.go for rationale. We
	// use AllAssets (not ActiveAssets) so historical bars for
	// since-delisted tickers still resolve via per-date lookup.
	dbAssets, err := data.AllAssets(ctx, conn)

	conn.Release()

	if err != nil {
		logger.Error().Err(err).Msg("could not load assets")

		runSummary.Status = data.RunFailed

		return
	}

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	universe := buildHistoricalUniverse(dbAssets, tickerFilter, figiFilter)

	if universe.tickerCount() == 0 {
		logger.Warn().Msg("no assets in scope for massive 1-minute; skipping run")
		return
	}

	logger.Info().
		Time("start", start).
		Time("end", end).
		Int("scope_tickers", universe.tickerCount()).
		Msg("massive flat-files 1-minute loader starting")

	process := func(ctx context.Context, d time.Time) (int, error) {
		return streamMinuteAggsForDate(ctx, s3Client, sub, universe, d, out)
	}

	workers := pickFlatFileWorkers(logger, start, end, "minute_aggs")

	n, err := downloadDailyAggsRange(ctx, logger, "minute_aggs", workers, start, end, process)
	if err != nil {
		runSummary.Status = data.RunFailed
		return
	}

	numObs += n
}

// streamMinuteAggsForDate downloads, parses, and publishes 1-minute
// bars for date d. Rows whose ticker had no figi in the universe AS
// OF date d are dropped. A NoSuchKey response (non-trading day, or
// file not yet published) is treated as an empty result rather than
// an error. Transient transport / mid-stream failures are retried
// with exponential backoff before giving up on the date.
func streamMinuteAggsForDate(ctx context.Context, client *s3.Client, sub *library.Subscription, universe *historicalAssetUniverse, d time.Time, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)
	key := minuteAggsKey(d)

	rows, err := fetchAndParseAggs(ctx, client, key)
	if err != nil {
		if errors.Is(err, errFlatFileMissing) {
			logger.Debug().Str("key", key).Msg("flat file not present; skipping date")
			return 0, nil
		}

		return 0, err
	}

	// Optional parquet backup of the source file. Failures are logged
	// but do not abort the run.
	if backupDir := strings.TrimSpace(viper.GetString("parquet_backup_dir")); backupDir != "" {
		destPath := backupPathFor(filepath.Join(backupDir, subscriptionBackupSlug(sub)), d)

		exists, statErr := backupExists(destPath)
		switch {
		case statErr != nil:
			logger.Warn().Err(statErr).Str("path", destPath).Msg("could not stat backup destination; skipping backup")
		case exists:
			logger.Debug().Str("path", destPath).Msg("parquet backup already present; skipping")
		default:
			if err := writeFlatFileBackup(destPath, rows); err != nil {
				logger.Warn().Err(err).Str("path", destPath).Msg("parquet backup failed")
			} else {
				logger.Info().Str("path", destPath).Int("rows", len(rows)).Msg("wrote parquet backup")
			}
		}
	}

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
