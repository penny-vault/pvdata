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

type Estimate struct {
	Ticker        string
	CompositeFigi string
	EventDate     time.Time
	Series        string
	Value         float64
	NumAnalysts   int
	StdDev        float64
}

func (estimate *Estimate) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if estimate.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing estimate transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"event_date",
		"series",
		"value",
		"num_analysts",
		"std_dev"
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		ticker = EXCLUDED.ticker,
		value = EXCLUDED.value,
		num_analysts = EXCLUDED.num_analysts,
		std_dev = EXCLUDED.std_dev`, tbl)

	_, err = tx.Exec(ctx, sql,
		estimate.Ticker,
		estimate.CompositeFigi,
		estimate.EventDate,
		estimate.Series,
		estimate.Value,
		estimate.NumAnalysts,
		estimate.StdDev,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save estimate to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
