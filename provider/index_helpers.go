package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// indexMember represents a constituent of an index with its FIGI and weight.
type indexMember struct {
	CompositeFigi string
	Weight        float64
}

const weightChangeThreshold = 0.01

// shouldTakeSnapshot returns true if a new snapshot should be taken based on the
// configured frequency, the date of the last snapshot, and the current processing date.
func shouldTakeSnapshot(lastSnapshotDate, currentDate time.Time, frequency string) bool {
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

	return currentDate.Sub(lastSnapshotDate) >= interval
}

// diffSnapshots compares current holdings against previous holdings
// and returns maps of added, removed, and weight-changed tickers.
// A weight change is significant when the absolute difference exceeds weightChangeThreshold.
func diffSnapshots(current, previous map[string]indexMember) (added, removed, weightChanged map[string]indexMember) {
	added = make(map[string]indexMember)
	removed = make(map[string]indexMember)
	weightChanged = make(map[string]indexMember)

	for ticker, member := range current {
		prev, ok := previous[ticker]
		if !ok {
			added[ticker] = member
			continue
		}

		delta := member.Weight - prev.Weight
		if delta < 0 {
			delta = -delta
		}

		if delta >= weightChangeThreshold-1e-9 {
			weightChanged[ticker] = member
		}
	}

	for ticker, member := range previous {
		if _, ok := current[ticker]; !ok {
			removed[ticker] = member
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
func emitChangelog(adds, removes map[string]indexMember, indexName string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, member := range adds {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "add",
				Weight:        member.Weight,
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}

	for ticker, member := range removes {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
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
