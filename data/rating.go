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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type AnalystRating struct {
	Ticker        string    `db:"ticker"`
	CompositeFigi string    `db:"composite_figi"`
	EventDate     time.Time `db:"event_date"`
	Analyst       string    `db:"analyst"`

	// A rating of 1 means buy or strong buy, 2 means outperform, 3 means hold,
	// 4 means underperform and 5 means sell.
	Rating int
}

func (rating *AnalystRating) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if rating.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing asset transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"event_date",
		"analyst",
		"rating"
	) VALUES (
		$1, $2, $3, $4, $5
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		rating = EXCLUDED.rating`, tbl)

	_, err = tx.Exec(ctx, sql, rating.Ticker, rating.CompositeFigi, rating.EventDate, rating.Analyst, rating.Rating)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save analyst rating to DB failed")

		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return nil
}

func LatestRating(ctx context.Context, tbl string, dbConn *pgxpool.Conn, analyst string) *AnalystRating {
	rows, err := dbConn.Query(ctx, "SELECT * FROM "+tbl+" WHERE analyst=$1 ORDER BY event_date DESC LIMIT 1", analyst)
	if err != nil {
		log.Error().Err(err).Msg("error querying for latest rating")
		return nil
	}

	rating, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[AnalystRating])
	if err != nil {
		return nil
	}

	return &rating
}
