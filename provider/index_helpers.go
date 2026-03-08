package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// shouldTakeSnapshot returns true if a new snapshot should be taken based on the
// configured frequency and the date of the last snapshot.
func shouldTakeSnapshot(lastSnapshotDate time.Time, frequency string) bool {
	if lastSnapshotDate.IsZero() {
		return true
	}

	var interval time.Duration
	switch frequency {
	case "daily":
		interval = 24 * time.Hour
	case "weekly":
		interval = 7 * 24 * time.Hour
	case "monthly":
		interval = 30 * 24 * time.Hour
	case "quarterly":
		interval = 90 * 24 * time.Hour
	default:
		interval = 7 * 24 * time.Hour
	}

	return time.Since(lastSnapshotDate) >= interval
}

// diffSnapshots compares current holdings (ticker->figi) against previous holdings
// and returns maps of added and removed tickers.
func diffSnapshots(current, previous map[string]string) (added, removed map[string]string) {
	added = make(map[string]string)
	removed = make(map[string]string)

	for ticker, figi := range current {
		if _, ok := previous[ticker]; !ok {
			added[ticker] = figi
		}
	}

	for ticker, figi := range previous {
		if _, ok := current[ticker]; !ok {
			removed[ticker] = figi
		}
	}

	return
}

// lastSnapshotDate queries the database for the most recent snapshot date for the given index.
func lastSnapshotDate(ctx context.Context, pool *pgxpool.Pool, table, indexName string) time.Time {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for lastSnapshotDate")
		return time.Time{}
	}
	defer conn.Release()

	var snapshotDate time.Time
	sql := fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s_snapshot WHERE index_name = $1`, table)
	err = conn.QueryRow(ctx, sql, indexName).Scan(&snapshotDate)
	if err != nil {
		log.Error().Err(err).Msg("could not query last snapshot date")
		return time.Time{}
	}
	return snapshotDate
}

// previousSnapshotTickers queries the database for all tickers in the most recent
// snapshot for the given index name, returning a map of ticker->compositeFigi.
func previousSnapshotTickers(ctx context.Context, pool *pgxpool.Pool, table, indexName string) map[string]string {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for previousSnapshotTickers")
		return map[string]string{}
	}
	defer conn.Release()

	sql := fmt.Sprintf(`SELECT ticker, composite_figi FROM %s_snapshot
		WHERE index_name = $1 AND snapshot_date = (
			SELECT MAX(snapshot_date) FROM %s_snapshot WHERE index_name = $1
		)`, table, table)

	rows, err := conn.Query(ctx, sql, indexName)
	if err != nil {
		log.Error().Err(err).Msg("could not query previous snapshot tickers")
		return map[string]string{}
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var ticker, figi string
		if err := rows.Scan(&ticker, &figi); err != nil {
			log.Error().Err(err).Msg("error scanning previous snapshot row")
			continue
		}
		result[ticker] = figi
	}
	return result
}

// emitChangelog emits IndexChange observations for adds and removes.
func emitChangelog(adds, removes map[string]string, indexName string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, figi := range adds {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: figi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "add",
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}

	for ticker, figi := range removes {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: figi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "remove",
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}
}
