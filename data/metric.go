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

type Metric struct {
	Ticker          string
	CompositeFigi   string
	EventDate       time.Time
	MarketCap       int64
	EV              int64
	PE              float64
	PB              float64
	PS              float64
	EVtoEBIT        float64
	EVtoEBITDA      float64
	PEForward       float64
	PEG             float64
	PriceToCashFlow float64
	Beta            float64
}

func (metric *Metric) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if metric.Ticker == "" || metric.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing metric transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"event_date",
		"market_cap",
		"ev",
		"pe",
		"pb",
		"ps",
		"ev_ebit",
		"ev_ebitda",
		"pe_forward",
		"peg",
		"price_to_cash_flow",
		"beta"
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		ticker = EXCLUDED.ticker,
		market_cap = EXCLUDED.market_cap,
		ev = EXCLUDED.ev,
		pe = EXCLUDED.pe,
		pb = EXCLUDED.pb,
		ps = EXCLUDED.ps,
		ev_ebit = EXCLUDED.ev_ebit,
		ev_ebitda = EXCLUDED.ev_ebitda,
		pe_forward = EXCLUDED.pe_forward,
		peg = EXCLUDED.peg,
		price_to_cash_flow = EXCLUDED.price_to_cash_flow,
		beta = EXCLUDED.beta`, tbl)

	_, err = tx.Exec(ctx, sql,
		metric.Ticker,
		metric.CompositeFigi,
		metric.EventDate,
		metric.MarketCap,
		metric.EV,
		metric.PE,
		metric.PB,
		metric.PS,
		metric.EVtoEBIT,
		metric.EVtoEBITDA,
		metric.PEForward,
		metric.PEG,
		metric.PriceToCashFlow,
		metric.Beta,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Object("Metric", metric).Msg("save metric to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}

func (metric *Metric) MarshalZerologObject(e *zerolog.Event) {
	e.Str("Ticker", metric.Ticker)
	e.Str("CompositeFigi", metric.CompositeFigi)
	e.Time("Date", metric.EventDate)
}
