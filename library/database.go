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
package library

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

type Library struct {
	DBUrl string
	Name  string
	Owner string

	Pool *pgxpool.Pool
}

// Connect to the database configured for the library
func (myLibrary *Library) Connect(ctx context.Context) error {
	if myLibrary.Pool != nil {
		return nil
	}

	pool, err := pgxpool.New(context.Background(), myLibrary.DBUrl)
	if err != nil {
		return err
	}

	myLibrary.Pool = pool

	return nil
}

// Close the database pool
func (myLibrary *Library) Close() {
	myLibrary.Pool.Close()
}

// NewFromDB creates a new library object with values from the database
func NewFromDB(ctx context.Context, dbURL string) (*Library, error) {
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		return nil, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	myLibrary := Library{
		DBUrl: dbURL,
		Pool:  pool,
	}

	if err := conn.QueryRow(ctx, "SELECT name, owner FROM library").Scan(&myLibrary.Name, &myLibrary.Owner); err != nil {
		return nil, err
	}

	return &myLibrary, nil
}

// SaveDB creates a new record in the library table for this library
func (myLibrary *Library) SaveDB(ctx context.Context) error {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, `INSERT INTO library ("name", "owner") VALUES ($1, $2)`, myLibrary.Name, myLibrary.Owner)

	return err
}

// NumSubscriptions returns the total count of subscriptions configured in the database
func (myLibrary *Library) NumSubscriptions(ctx context.Context) (int, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	count := 0
	err = conn.QueryRow(ctx, "SELECT count(*) FROM subscriptions WHERE active='t'").Scan(&count)

	return count, err
}

// LastUpdated returns the date that the database was last updated
func (myLibrary *Library) LastUpdated(ctx context.Context) (time.Time, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Release()

	var lastUpdated time.Time

	err = conn.QueryRow(ctx, "SELECT coalesce(max(last_run), '0001-01-01'::timestamp) FROM subscriptions WHERE active='t'").Scan(&lastUpdated)
	if err != nil {
		return time.Time{}, err
	}

	return lastUpdated, nil
}

// TotalRecords returns the total number of records in the library
func (myLibrary *Library) TotalRecords(ctx context.Context) (int, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	count := 0
	err = conn.QueryRow(ctx, "SELECT coalesce(sum(total_records), 0) FROM subscriptions WHERE active='t'").Scan(&count)

	return count, err
}

// TotalSecurities returns the total number of securities in the library
func (myLibrary *Library) TotalSecurities(ctx context.Context) (int, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	count := 0
	err = conn.QueryRow(ctx, "SELECT coalesce(sum(total_records), 0) FROM subscriptions WHERE active='t'").Scan(&count)

	return count, err
}

// SaveObservations continuously reads from the input queue
func (myLibrary *Library) SaveObservations(queue <-chan *data.Observation, wg *sync.WaitGroup, validator *checks.InlineValidator) {
	ctx := context.Background()

	defer wg.Done()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Panic().Err(err).Msg("cannot acquire database connection")
		return
	}
	defer conn.Release()

	subscriptionList, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not get list of subscriptions")
	}

	subscriptions := make(map[uuid.UUID]*Subscription, len(subscriptionList))
	for _, sub := range subscriptionList {
		subscriptions[sub.ID] = sub
	}

	for elem := range queue {
		subscription, ok := subscriptions[elem.SubscriptionID]
		if !ok {
			log.Error().Str("SubscriptionID", elem.SubscriptionID.String()).Str("SubscriptionName", elem.SubscriptionName).Msg("subscription not found")
			continue
		}

		var filer data.Filer
		if filerPath, ok := subscription.Config["filer"]; ok {
			filer = data.NewFilerFromString(filerPath)
		}

		if validator != nil {
			results, block := validator.Validate(ctx, elem)
			if len(results) > 0 {
				if err := checks.SaveResults(ctx, myLibrary.Pool, results, elem.SubscriptionID, uuid.Nil); err != nil {
					log.Error().Err(err).Msg("failed to save check results")
				}

				for _, r := range results {
					log.Warn().
						Str("check", r.CheckName).
						Str("severity", r.Severity.String()).
						Str("ticker", r.Ticker).
						Str("dimension", r.Dimension).
						Str("field", r.Field).
						Msg(r.Message)
				}
			}

			if block {
				blockedTicker := elem.SubscriptionName
				blockedDimension := ""

				if len(results) > 0 {
					blockedTicker = results[0].Ticker
					blockedDimension = results[0].Dimension
				}

				log.Error().
					Str("ticker", blockedTicker).
					Str("dimension", blockedDimension).
					Str("subscription", elem.SubscriptionName).
					Msg("observation blocked by inline check")

				continue
			}
		}

		if elem.AssetObject != nil && subscription.DataTablesMap[data.AssetKey] != "" {
			if filer != nil {
				err := elem.AssetObject.SaveFiles(ctx, filer)
				if err != nil {
					log.Error().Err(err).Msg("cannot save asset files")
					continue
				}
			}

			if err := elem.AssetObject.SaveDB(ctx, subscription.DataTablesMap[data.AssetKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save asset to database")
			}
		}

		if elem.CustomObject != nil && subscription.DataTablesMap[data.CustomKey] != "" {
			if err := elem.CustomObject.SaveDB(ctx, subscription.DataTablesMap[data.CustomKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save custom data to database")
			}
		}

		if elem.EconomicIndicator != nil && subscription.DataTablesMap[data.EconomicIndicatorKey] != "" {
			if err := elem.EconomicIndicator.SaveDB(ctx, subscription.DataTablesMap[data.EconomicIndicatorKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save economic indicator to database")
			}
		}

		if elem.EodQuote != nil && subscription.DataTablesMap[data.EODKey] != "" {
			if err := elem.EodQuote.SaveDB(ctx, subscription.DataTablesMap[data.EODKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save eod quote to database")
			}
		}

		if elem.Fundamental != nil && subscription.DataTablesMap[data.FundamentalsKey] != "" {
			if err := elem.Fundamental.SaveDB(ctx, subscription.DataTablesMap[data.FundamentalsKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save fundamental to database")
			}
		}

		if elem.IndexSnapshot != nil && subscription.DataTablesMap[data.IndexSnapshotKey] != "" {
			if err := elem.IndexSnapshot.SaveDB(ctx, subscription.DataTablesMap[data.IndexSnapshotKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save index snapshot to database")
			}
		}

		if elem.IndexChange != nil && subscription.DataTablesMap[data.IndexChangelogKey] != "" {
			if err := elem.IndexChange.SaveDB(ctx, subscription.DataTablesMap[data.IndexChangelogKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save index change to database")
			}
		}

		if elem.MarketHoliday != nil && subscription.DataTablesMap[data.MarketHolidaysKey] != "" {
			if err := elem.MarketHoliday.SaveDB(ctx, subscription.DataTablesMap[data.MarketHolidaysKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save market holiday to database")
			}
		}

		if elem.Metric != nil && subscription.DataTablesMap[data.MetricKey] != "" {
			if err := elem.Metric.SaveDB(ctx, subscription.DataTablesMap[data.MetricKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save metric to database")
			}
		}

		if elem.Rating != nil && subscription.DataTablesMap[data.RatingKey] != "" {
			if err := elem.Rating.SaveDB(ctx, subscription.DataTablesMap[data.RatingKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save rating to database")
			}
		}

		if elem.Consensus != nil && subscription.DataTablesMap[data.ConsensusKey] != "" {
			if err := elem.Consensus.SaveDB(ctx, subscription.DataTablesMap[data.ConsensusKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save consensus to database")
			}
		}

		if elem.Estimate != nil && subscription.DataTablesMap[data.EstimateKey] != "" {
			if err := elem.Estimate.SaveDB(ctx, subscription.DataTablesMap[data.EstimateKey], conn); err != nil {
				log.Error().Err(err).Msg("cannot save estimate to database")
			}
		}
	}
}

// Subscriptions returns an array of subscription objects
func (myLibrary *Library) Subscriptions(ctx context.Context) ([]*Subscription, error) {
	var subscriptions []*Subscription

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Panic().Err(err).Msg("could not load timezone")
	}

	scheduler, err := gocron.NewScheduler(
		gocron.WithLocation(nyc),
	)
	if err != nil {
		log.Panic().Err(err).Msg("could not create scheduler")
	}

	scheduler.Start()

	err = pgxscan.Select(ctx, myLibrary.Pool, &subscriptions,
		`SELECT id, name, provider, dataset, config, data_tables, data_types, total_records,
num_records_last_import, total_securities, num_securities_last_import,
coalesce(first_obs_date, '0001-01-01'::timestamp) as first_obs_date,
coalesce(last_obs_date, '0001-01-01'::timestamp) as last_obs_date, schedule, health_check_id,
coalesce(last_run, '0001-01-01'::timestamp) as last_run, active, schema_version, created_on,
created_by FROM subscriptions`)
	for _, sub := range subscriptions {
		sub.Library = myLibrary

		sub.DataTablesMap = make(map[string]string, len(sub.DataTables))
		for idx, dataType := range sub.DataTypes {
			sub.DataTablesMap[dataType] = sub.DataTables[idx]
		}

		if sub.Schedule == "" {
			continue
		}

		job, err := scheduler.NewJob(gocron.CronJob(sub.Schedule, false), gocron.NewTask(func() {}))
		if err != nil {
			log.Warn().Err(err).Str("schedule", sub.Schedule).Str("subscription", sub.Name).Msg("could not create cron job")
			continue
		}

		sub.NextRun, err = job.NextRun()
		if err != nil {
			log.Warn().Err(err).Msg("could not get next run time")
			continue
		}

		sub.NextRunHuman = strings.Replace(time.Until(sub.NextRun).Round(time.Minute).String(), "0s", "", 1)
	}

	if shutdownErr := scheduler.Shutdown(); shutdownErr != nil {
		log.Warn().Err(shutdownErr).Msg("error shutting down scheduler")
	}

	return subscriptions, err
}

// SubscriptionFromID fetches a subscription from the library with the given ID
func (myLibrary *Library) SubscriptionFromID(ctx context.Context, id string) (*Subscription, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	subscription := &Subscription{
		Library: myLibrary,
	}

	// Try UUID prefix match first
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT id, name, provider, dataset, config,
	data_tables, data_types, total_records, num_records_last_import, total_securities,
	num_securities_last_import, coalesce(first_obs_date, '0001-01-01'::timestamp) as first_obs_date,
	coalesce(last_obs_date, '0001-01-01'::timestamp) as last_obs_date,
	schedule, health_check_id, coalesce(last_run, '0001-01-01'::timestamp) as last_run, active,
	schema_version, created_on, created_by FROM subscriptions WHERE id::text like '%s%%' LIMIT 1`, id))
	if err != nil {
		return nil, err
	}

	err = pgxscan.ScanOne(subscription, rows)
	if err == nil {
		subscription.DataTablesMap = make(map[string]string, len(subscription.DataTables))
		for idx, dataType := range subscription.DataTypes {
			subscription.DataTablesMap[dataType] = subscription.DataTables[idx]
		}

		return subscription, nil
	}

	// Fallback: try case-insensitive name match
	rows, err = conn.Query(ctx, `SELECT id, name, provider, dataset, config,
	data_tables, data_types, total_records, num_records_last_import, total_securities,
	num_securities_last_import, coalesce(first_obs_date, '0001-01-01'::timestamp) as first_obs_date,
	coalesce(last_obs_date, '0001-01-01'::timestamp) as last_obs_date,
	schedule, health_check_id, coalesce(last_run, '0001-01-01'::timestamp) as last_run, active,
	schema_version, created_on, created_by FROM subscriptions WHERE lower(name) = lower($1) LIMIT 1`, id)
	if err != nil {
		return nil, err
	}

	err = pgxscan.ScanOne(subscription, rows)
	if err != nil {
		return nil, fmt.Errorf("subscription not found (tried ID prefix and name match): %s", id)
	}

	subscription.DataTablesMap = make(map[string]string, len(subscription.DataTables))
	for idx, dataType := range subscription.DataTypes {
		subscription.DataTablesMap[dataType] = subscription.DataTables[idx]
	}

	return subscription, nil
}
