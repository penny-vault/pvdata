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

// metricUpsertSQL is the per-row INSERT used by SaveMetricsBatch. The
// table name is injected with fmt.Sprintf at flush time; values are
// bound as positional parameters.
const metricUpsertSQL = `INSERT INTO %[1]s (
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
	beta = EXCLUDED.beta`

// SaveMetricsBatch upserts a slice of metrics using pgx.Batch with no
// outer transaction, so each statement runs in its own implicit
// server-side transaction. A row that fails its CHECK or unique key is
// logged without aborting the others. Returns the first error seen.
func SaveMetricsBatch(ctx context.Context, tbl string, dbConn *pgxpool.Conn, metrics []*Metric) error {
	if len(metrics) == 0 {
		return nil
	}

	sql := fmt.Sprintf(metricUpsertSQL, tbl)

	batch := &pgx.Batch{}
	queued := make([]*Metric, 0, len(metrics))

	for _, m := range metrics {
		if m.Ticker == "" || m.CompositeFigi == "" {
			continue
		}

		batch.Queue(sql,
			m.Ticker,
			m.CompositeFigi,
			m.EventDate,
			m.MarketCap,
			m.EV,
			m.PE,
			m.PB,
			m.PS,
			m.EVtoEBIT,
			m.EVtoEBITDA,
			m.PEForward,
			m.PEG,
			m.PriceToCashFlow,
			m.Beta,
		)
		queued = append(queued, m)
	}

	if len(queued) == 0 {
		return nil
	}

	results := dbConn.SendBatch(ctx, batch)
	defer results.Close()

	var firstErr error

	for i := range queued {
		if _, err := results.Exec(); err != nil {
			log.Error().Err(err).Object("Metric", queued[i]).Msg("save metric to DB failed")

			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

func (metric *Metric) MarshalZerologObject(e *zerolog.Event) {
	e.Str("Ticker", metric.Ticker)
	e.Str("CompositeFigi", metric.CompositeFigi)
	e.Time("Date", metric.EventDate)
}
