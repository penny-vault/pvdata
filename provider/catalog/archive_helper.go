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
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"

	"github.com/penny-vault/pvdata/library"
)

// eodArchiveForRun returns the per-fetcher EOD archive index, loaded
// lazily on first call. The archive lives under
// parquet_backup_dir/<eod_subscription_slug>, where eod_subscription
// is the Massive subscription whose data_types include "eod" — not
// the current fetcher's asset-description subscription. Returns nil
// when parquet_backup_dir is unset, the EOD subscription cannot be
// located, or the archive directory does not exist.
func (api *massiveAssetFetcher) eodArchiveForRun() *EODArchive {
	api.eodArchiveOnce.Do(func() {
		baseDir := strings.TrimSpace(viper.GetString("parquet_backup_dir"))
		if baseDir == "" {
			return
		}

		eodSub, err := api.findEODSubscription()
		if err != nil {
			log.Warn().Err(err).Msg("massive: could not locate EOD subscription; asset builder will run without EOD candidates")

			return
		}

		if eodSub == nil {
			return
		}

		rootDir := filepath.Join(baseDir, subscriptionBackupSlug(eodSub))

		archive, err := loadOrReuseEODArchive(rootDir)
		if err != nil {
			log.Warn().
				Err(err).
				Str("Root", rootDir).
				Msg("massive: eod archive load failed; asset builder will run without EOD candidates")

			return
		}

		coverageStart, coverageEnd := archive.Coverage()
		log.Info().
			Str("Root", rootDir).
			Int("Tickers", archive.TickerCount()).
			Time("CoverageStart", coverageStart).
			Time("CoverageEnd", coverageEnd).
			Msg("massive: eod archive loaded for asset builder")

		api.eodArchive = archive
	})

	return api.eodArchive
}

// findEODSubscription returns the Massive subscription whose
// data_types include "eod". Returns (nil, nil) when no such
// subscription exists; a non-nil error surfaces a DB lookup failure
// so the caller can log it.
func (api *massiveAssetFetcher) findEODSubscription() (*library.Subscription, error) {
	if api.subscription == nil || api.subscription.Library == nil {
		return nil, nil
	}

	subs, err := api.subscription.Library.Subscriptions(context.Background())
	if err != nil {
		return nil, err
	}

	for _, sub := range subs {
		if sub == nil || sub.Provider != "massive" {
			continue
		}

		for _, dt := range sub.DataTypes {
			if dt == "eod" {
				return sub, nil
			}
		}
	}

	return nil, nil
}

// parseISODate parses a YYYY-MM-DD string and returns the zero
// time.Time on empty input or parse failure. Used by callers that
// take date strings from data.Asset.ListingDate / DelistingDate (the
// SaveDB-facing string form) and need them as time.Time for date
// math.
func parseISODate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}

	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}

	return t
}
