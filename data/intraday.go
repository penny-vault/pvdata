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

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type IntradayBar struct {
	Date          time.Time `json:"date"`
	Ticker        string    `json:"ticker"`
	CompositeFigi string    `json:"compositeFigi"`
	Open          float64   `json:"open"`
	High          float64   `json:"high"`
	Low           float64   `json:"low"`
	Close         float64   `json:"close"`
	Volume        float64   `json:"volume"`
}

// SaveIntradayBarsBatch streams a batch of intraday bars into ClickHouse via
// the native column-oriented protocol. ClickHouse's ReplacingMergeTree
// dedupes on (composite_figi, event_date) at merge time, giving last-write-
// wins semantics equivalent to the previous Postgres ON CONFLICT path.
func SaveIntradayBarsBatch(ctx context.Context, tbl string, conn driver.Conn, bars []*IntradayBar) error {
	if len(bars) == 0 {
		return nil
	}

	batch, err := conn.PrepareBatch(ctx, fmt.Sprintf(
		"INSERT INTO %s (ticker, composite_figi, event_date, open, high, low, close, volume)", tbl))
	if err != nil {
		return fmt.Errorf("prepare intraday batch: %w", err)
	}

	for _, bar := range bars {
		if err := batch.Append(
			bar.Ticker,
			bar.CompositeFigi,
			bar.Date,
			bar.Open,
			bar.High,
			bar.Low,
			bar.Close,
			bar.Volume,
		); err != nil {
			return fmt.Errorf("append intraday row: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("send intraday batch: %w", err)
	}

	return nil
}
