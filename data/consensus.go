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
package data

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type Consensus struct {
	Ticker              string
	CompositeFigi       string
	EventDate           time.Time
	AvgRecommendation   float64
	NumAnalysts         int
	NumStrongBuyOrBuy   int
	NumHold             int
	NumSellOrStrongSell int
	NumUpgrades         int
	NumDowngrades       int
	AvgTargetPrice      float64
}

func (consensus *Consensus) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if consensus.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing consensus transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"event_date",
		"avg_recommendation",
		"num_analysts",
		"num_strong_buy_or_buy",
		"num_hold",
		"num_sell_or_strong_sell",
		"num_upgrades",
		"num_downgrades",
		"avg_target_price"
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		ticker = EXCLUDED.ticker,
		avg_recommendation = EXCLUDED.avg_recommendation,
		num_analysts = EXCLUDED.num_analysts,
		num_strong_buy_or_buy = EXCLUDED.num_strong_buy_or_buy,
		num_hold = EXCLUDED.num_hold,
		num_sell_or_strong_sell = EXCLUDED.num_sell_or_strong_sell,
		num_upgrades = EXCLUDED.num_upgrades,
		num_downgrades = EXCLUDED.num_downgrades,
		avg_target_price = EXCLUDED.avg_target_price`, tbl)

	_, err = tx.Exec(ctx, sql,
		consensus.Ticker,
		consensus.CompositeFigi,
		consensus.EventDate,
		consensus.AvgRecommendation,
		consensus.NumAnalysts,
		consensus.NumStrongBuyOrBuy,
		consensus.NumHold,
		consensus.NumSellOrStrongSell,
		consensus.NumUpgrades,
		consensus.NumDowngrades,
		consensus.AvgTargetPrice,
	)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save consensus to DB failed")

		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
