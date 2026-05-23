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
package checks

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EODProviderConsistency compares OHLCV values for the same
// (composite_figi, event_date) across every subscription that
// produces EOD data. A mismatch beyond the configured tolerance is
// reported as a check finding so the operator can investigate which
// provider is wrong.
type EODProviderConsistency struct{}

// Tolerance constants for the OHLCV comparison. Prices pass if absolute
// difference is within priceAbsTolerance OR relative difference is
// within priceRelTolerance (covers late corrections and
// adjusted/unadjusted variance). Volume allows up to volumeRelTolerance
// relative difference (covers odd-lot/auction-volume counting variance).
const (
	priceAbsTolerance  = 0.01 // 1 cent
	priceRelTolerance  = 0.0001
	volumeRelTolerance = 0.005 // 0.5%
)

func (c *EODProviderConsistency) Name() string { return "eod_provider_consistency" }
func (c *EODProviderConsistency) Description() string {
	return "Compares OHLCV values for the same composite_figi/event_date across multiple EOD providers and flags disagreements beyond tolerance"
}
func (c *EODProviderConsistency) Phase() CheckPhase       { return PhaseAudit }
func (c *EODProviderConsistency) Severity() CheckSeverity { return SeverityWarning }
func (c *EODProviderConsistency) DataTypes() []string     { return []string{"eod"} }

// AuditAcrossProviders compares every pair of EOD tables. Comparisons
// are done in SQL for efficiency, with a per-pair LIMIT so a heavily
// inconsistent dataset cannot generate millions of findings.
func (c *EODProviderConsistency) AuditAcrossProviders(ctx context.Context, pool *pgxpool.Pool, _ string, tables []string, lookback *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	var results []CheckResult

	for i := range tables {
		for j := i + 1; j < len(tables); j++ {
			pairResults, pairErr := c.comparePair(ctx, conn, tables[i], tables[j], lookback)
			if pairErr != nil {
				return results, fmt.Errorf("compare %s vs %s: %w", tables[i], tables[j], pairErr)
			}

			results = append(results, pairResults...)
		}
	}

	return results, nil
}

// maxFindingsPerPair caps how many disagreements a single pairwise
// comparison can emit. A large gap (e.g. one provider missing a whole
// year) would otherwise produce millions of findings; the cap forces
// the operator to investigate via the data_quality_issues table
// rather than pull the firehose.
const maxFindingsPerPair = 500

func (c *EODProviderConsistency) comparePair(ctx context.Context, conn *pgxpool.Conn, tableA, tableB string, lookback *time.Duration) ([]CheckResult, error) {
	var args []any

	whereClause := ""

	if lookback != nil {
		args = append(args, time.Now().UTC().Add(-*lookback))
		whereClause = fmt.Sprintf(" AND a.event_date > $%d AND b.event_date > $%d", len(args), len(args))
	}

	query := fmt.Sprintf(`
		SELECT
		  a.ticker,
		  a.composite_figi,
		  a.event_date,
		  a.open::float8,  b.open::float8,
		  a.high::float8,  b.high::float8,
		  a.low::float8,   b.low::float8,
		  a.close::float8, b.close::float8,
		  a.volume,        b.volume
		FROM %s a
		JOIN %s b
		  ON a.composite_figi = b.composite_figi
		 AND a.event_date     = b.event_date
		WHERE 1=1%s
		LIMIT %d`, tableA, tableB, whereClause, maxFindingsPerPair*20)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var (
			ticker, figi                                           string
			eventDate                                              time.Time
			openA, openB, highA, highB, lowA, lowB, closeA, closeB float64
			volumeA, volumeB                                       int64
		)

		if err := rows.Scan(&ticker, &figi, &eventDate,
			&openA, &openB, &highA, &highB, &lowA, &lowB, &closeA, &closeB,
			&volumeA, &volumeB); err != nil {
			return nil, err
		}

		findings := comparePriceFields(tableA, tableB, ticker, figi, eventDate,
			openA, openB, highA, highB, lowA, lowB, closeA, closeB)

		results = append(results, findings...)

		if vfind := compareVolume(tableA, tableB, ticker, figi, eventDate, volumeA, volumeB); vfind != nil {
			results = append(results, *vfind)
		}

		if len(results) >= maxFindingsPerPair {
			break
		}
	}

	return results, rows.Err()
}

// comparePriceFields tests open/high/low/close pairs against the
// price tolerance. Returns one CheckResult per disagreeing field.
func comparePriceFields(tableA, tableB, ticker, figi string, eventDate time.Time,
	openA, openB, highA, highB, lowA, lowB, closeA, closeB float64) []CheckResult {
	type pricePair struct {
		field  string
		valueA float64
		valueB float64
	}

	pairs := []pricePair{
		{"open", openA, openB},
		{"high", highA, highB},
		{"low", lowA, lowB},
		{"close", closeA, closeB},
	}

	var out []CheckResult

	for _, p := range pairs {
		if pricesAgree(p.valueA, p.valueB) {
			continue
		}

		out = append(out, CheckResult{
			CheckName:     "eod_provider_consistency",
			Severity:      SeverityWarning,
			Ticker:        ticker,
			CompositeFigi: figi,
			EventDate:     eventDate,
			Field:         p.field,
			Message: fmt.Sprintf("%s mismatch between %s and %s",
				p.field, tableA, tableB),
			Expected: fmt.Sprintf("%s=%.4f", tableA, p.valueA),
			Actual:   fmt.Sprintf("%s=%.4f", tableB, p.valueB),
			DataType: "eod",
		})
	}

	return out
}

func compareVolume(tableA, tableB, ticker, figi string, eventDate time.Time, volumeA, volumeB int64) *CheckResult {
	if volumesAgree(volumeA, volumeB) {
		return nil
	}

	return &CheckResult{
		CheckName:     "eod_provider_consistency",
		Severity:      SeverityWarning,
		Ticker:        ticker,
		CompositeFigi: figi,
		EventDate:     eventDate,
		Field:         "volume",
		Message:       fmt.Sprintf("volume mismatch between %s and %s", tableA, tableB),
		Expected:      fmt.Sprintf("%s=%d", tableA, volumeA),
		Actual:        fmt.Sprintf("%s=%d", tableB, volumeB),
		DataType:      "eod",
	}
}

// pricesAgree returns true when two price values are close enough to
// be considered equivalent for this check. Two prices agree when
// either the absolute difference is within priceAbsTolerance or the
// relative difference is within priceRelTolerance - whichever is more
// generous, since 1 cent is meaningful for a $5 stock but trivial for
// a $5000 stock.
func pricesAgree(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff <= priceAbsTolerance {
		return true
	}

	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return diff == 0
	}

	return diff/scale <= priceRelTolerance
}

// volumesAgree returns true when two volume values are within the
// configured relative tolerance. Exact equality is the common case;
// the tolerance covers odd-lot / auction-volume differences.
func volumesAgree(a, b int64) bool {
	if a == b {
		return true
	}

	scale := math.Max(math.Abs(float64(a)), math.Abs(float64(b)))
	if scale == 0 {
		return false
	}

	diff := math.Abs(float64(a - b))

	return diff/scale <= volumeRelTolerance
}
