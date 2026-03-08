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
package provider

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

type EodAdjust struct{}

func (adjust *EodAdjust) Name() string {
	return "EOD Adjust"
}

func (adjust *EodAdjust) ConfigDescription() map[string]string {
	return map[string]string{
		"table": "What table do you want to adjust prices for?",
	}
}

func (adjust *EodAdjust) Description() string {
	return `Adjust end of day prices to account for dividends and stock splits`
}

func (adjust *EodAdjust) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"EOD Adjust": {
			Name:        "Adjust EOD Prices",
			Description: "Adjust end of day prices for dividends and stock splits",
			DataTypes:   []*data.DataType{data.DataTypes[data.EODKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: adjustPrices,
		},
	}
}

func adjustPrices(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
}

/*
func AdjustAssetEodPrice(ctx context.Context, conn PgxIface, compositeFigi string) ([]*data.Eod, error) {
	adjustHistory := make([]*data.Eod, 0)
	adjustFactor := 1.0

	rows, err := conn.Query(ctx, "SELECT event_date, ticker, composite_figi, close, dividend, split_factor FROM eod WHERE composite_figi = $1 ORDER BY ticker, event_date DESC", compositeFigi)
	if err != nil {
		log.Error().Err(err).Msg("SELECT all query error")
		return adjustHistory, err
	}

	for rows.Next() {
		var myEod Eod
		err = rows.Scan(&myEod.EventDate, &myEod.Ticker, &myEod.CompositeFigi, &myEod.Close, &myEod.Dividend, &myEod.SplitFactor)
		if err != nil {
			log.Error().Err(err).Msg("could not scan result into eod")
			return adjustHistory, err
		}

		myEod.AdjClose = myEod.Close / adjustFactor
		// CRSP adjustment calculations
		// see: http://crsp.org/products/documentation/crsp-calculations
		if myEod.Close > 0 {
			adjustFactor *= (1 + (myEod.Dividend / myEod.Close)) * myEod.SplitFactor
		} else {
			adjustFactor = 1
		}

		adjustHistory = append(adjustHistory, &myEod)
	}

	return adjustHistory, nil
}

// SaveAdjCloseToDb updates database record with adjusted close value
func SaveAdjCloseToDb(ctx context.Context, conn PgxIface, prices []*Eod) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not begin db transaction to adjust eod prices")
	}

	for _, myEod := range prices {
		if _, err := tx.Exec(ctx, "UPDATE eod SET adj_close=$1 WHERE composite_figi=$2 AND event_date=$3", myEod.AdjClose, myEod.CompositeFigi, myEod.EventDate); err != nil {
			log.Error().Err(err).Str("Ticker", myEod.Ticker).Float64("AdjustedClose", myEod.AdjClose).Float64("Close", myEod.Close).Time("EventDate", myEod.EventDate).Msg("failed to update eod")
			if err2 := tx.Rollback(ctx); err2 != nil {
				log.Error().Err(err2).Msg("failed to rollback db transaction")
			}
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Error().Err(err).Msg("could not commit eod price update to database")
		return err
	}

	return nil
}
*/
