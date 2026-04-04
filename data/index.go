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

// IndexConstituent represents a single member of an index at a point in time.
type IndexConstituent struct {
	Ticker        string  `json:"ticker"`
	CompositeFigi string  `json:"composite_figi"`
	Weight        float64 `json:"weight"`
}

// IndexSnapshot represents the full composition of an index at a point in time.
type IndexSnapshot struct {
	IndexTicker  string             `json:"index_ticker"`
	SnapshotDate time.Time          `json:"snapshot_date"`
	Constituents []IndexConstituent `json:"constituents"`
}

func (idx *IndexSnapshot) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if len(idx.Constituents) == 0 {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing index snapshot transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s_snapshot (
		"index_ticker",
		"snapshot_date",
		"constituents"
	) VALUES (
		$1, $2, $3
	) ON CONFLICT ON CONSTRAINT %[1]s_snapshot_pkey DO UPDATE SET
		constituents = EXCLUDED.constituents`, tbl)

	_, err = tx.Exec(ctx, sql,
		idx.IndexTicker,
		idx.SnapshotDate,
		idx.Constituents,
	)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save index snapshot to DB failed")

		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}

type IndexChange struct {
	Ticker        string
	CompositeFigi string
	IndexTicker   string
	EventDate     time.Time
	Action        string // "add" or "remove"
	Weight        float64
}

func (idx *IndexChange) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if idx.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing index change transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s_changelog (
		"composite_figi",
		"ticker",
		"index_ticker",
		"event_date",
		"action",
		"weight"
	) VALUES (
		$1, $2, $3, $4, $5, $6
	) ON CONFLICT ON CONSTRAINT %[1]s_changelog_pkey DO UPDATE SET
		action = EXCLUDED.action,
		weight = EXCLUDED.weight`, tbl)

	_, err = tx.Exec(ctx, sql,
		idx.CompositeFigi,
		idx.Ticker,
		idx.IndexTicker,
		idx.EventDate,
		idx.Action,
		idx.Weight,
	)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save index change to DB failed")

		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
