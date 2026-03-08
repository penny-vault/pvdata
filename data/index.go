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

type IndexSnapshot struct {
	Ticker        string
	CompositeFigi string
	IndexName     string
	SnapshotDate  time.Time
}

func (idx *IndexSnapshot) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if idx.CompositeFigi == "" {
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
		"composite_figi",
		"ticker",
		"index_name",
		"snapshot_date"
	) VALUES (
		$1, $2, $3, $4
	) ON CONFLICT ON CONSTRAINT %[1]s_snapshot_pkey DO NOTHING`, tbl)

	_, err = tx.Exec(ctx, sql,
		idx.CompositeFigi,
		idx.Ticker,
		idx.IndexName,
		idx.SnapshotDate,
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
	IndexName     string
	EventDate     time.Time
	Action        string // "add" or "remove"
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
		"index_name",
		"event_date",
		"action"
	) VALUES (
		$1, $2, $3, $4, $5
	) ON CONFLICT ON CONSTRAINT %[1]s_changelog_pkey DO UPDATE SET
		action = EXCLUDED.action`, tbl)

	_, err = tx.Exec(ctx, sql,
		idx.CompositeFigi,
		idx.Ticker,
		idx.IndexName,
		idx.EventDate,
		idx.Action,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save index change to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
