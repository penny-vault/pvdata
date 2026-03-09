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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type MarketHoliday struct {
	Name       string    `db:"holiday"`
	EventDate  time.Time `db:"event_date"`
	Market     string    `db:"market"`
	EarlyClose bool      `db:"early_close"`
	CloseTime  time.Time `db:"close_time"`
}

func (holiday *MarketHoliday) MarshalZerologObject(e *zerolog.Event) {
	e.Str("Name", holiday.Name)
	e.Time("EventDate", holiday.EventDate)
	e.Str("Market", holiday.Market)
	e.Bool("EarlyClose", holiday.EarlyClose)
	e.Time("CloseTime", holiday.CloseTime)
}

func (holiday *MarketHoliday) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing holiday transaction to database")
		}
	}()

	log.Debug().Object("MarketHoliday", holiday).Msg("Saving holiday to database")

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"holiday",
		"event_date",
		"market",
		"early_close",
		"close_time"
	) VALUES (
		$1, $2, $3, $4, $5
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		holiday = EXCLUDED.holiday,
		early_close = EXCLUDED.early_close,
		close_time = EXCLUDED.close_time`, tbl)

	_, err = tx.Exec(ctx, sql, holiday.Name, holiday.EventDate, holiday.Market, holiday.EarlyClose, holiday.CloseTime)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save market holiday to DB failed")
		return err
	}

	return nil
}
