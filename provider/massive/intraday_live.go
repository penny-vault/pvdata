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
package massive

import (
	"context"
	"fmt"
	"strings"
	"time"

	massivews "github.com/massive-com/client-go/v3/websocket"
	"github.com/massive-com/client-go/v3/websocket/models"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
)

const (
	// liveSessionEndHourET / liveSessionEndMinuteET bound the live
	// minute-bar session at 20:35 America/New_York, a ~35-minute buffer
	// after Nasdaq/NYSE after-hours close at 20:00. The cron fires once
	// per weekday in the morning; the Fetch holds the connection open
	// until this wall-clock deadline, then closes cleanly and returns
	// RunSuccess. Holidays produce a zero-bar session that still ends
	// at 20:35 with RunSuccess.
	liveSessionEndHourET   = 20
	liveSessionEndMinuteET = 35

	// liveMaxReconnects caps automatic reconnect attempts within a
	// single session. The Massive client retries indefinitely by
	// default; we want a failed session to surface as RunFailed so the
	// healthcheck pings down and the operator notices.
	liveMaxReconnects uint64 = 10

	// liveFeedRealTime / liveFeedDelayed select between the real-time
	// and 15-minute delayed endpoints. The subscription's `feed` config
	// key selects which one; unset defaults to real-time.
	liveFeedRealTime = "real-time"
	liveFeedDelayed  = "delayed"
)

// downloadMassiveMinuteLive runs one market-day streaming session
// against Massive's stocks websocket. It connects, authenticates,
// subscribes to AM.*, maps each incoming aggregate to an IntradayBar,
// and emits it through the standard observation channel. The session
// ends cleanly at 20:35 ET (RunSuccess) or on a fatal client error /
// reconnect exhaustion (RunFailed).
func downloadMassiveMinuteLive(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs

		if runSummary.Status != data.RunFailed {
			runSummary.Status = data.RunSuccess
		}

		exit <- runSummary
	}()

	apiKey := strings.TrimSpace(sub.Config["apiKey"])
	if apiKey == "" {
		logger.Error().Msg("massive live 1-minute requires apiKey")

		runSummary.Status = data.RunFailed

		return
	}

	feed, err := resolveLiveFeed(sub.Config["feed"])
	if err != nil {
		logger.Error().Err(err).Str("feed", sub.Config["feed"]).Msg("invalid feed; expected real-time or delayed")

		runSummary.Status = data.RunFailed

		return
	}

	deadline, err := liveSessionDeadline(time.Now())
	if err != nil {
		logger.Error().Err(err).Msg("could not compute session deadline")

		runSummary.Status = data.RunFailed

		return
	}

	if !deadline.After(time.Now()) {
		logger.Warn().Time("deadline", deadline).Msg("session deadline has already passed; skipping run")
		return
	}

	conn, err := sub.Library.AcquireWithTimeout(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("could not acquire database connection")

		runSummary.Status = data.RunFailed

		return
	}

	dbAssets, err := data.AllAssets(ctx, conn)

	conn.Release()

	if err != nil {
		logger.Error().Err(err).Msg("could not load assets")

		runSummary.Status = data.RunFailed

		return
	}

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	universe := data.NewAssetHistory(applySecurityFilter(dbAssets, tickerFilter, figiFilter))

	if universe.TickerCount() == 0 {
		logger.Warn().Msg("no assets in scope for massive live 1-minute; skipping run")
		return
	}

	sessionCtx, cancelSession := context.WithDeadline(ctx, deadline)
	defer cancelSession()

	maxRetries := liveMaxReconnects

	clientCfg := massivews.Config{
		APIKey:     apiKey,
		Feed:       feed,
		Market:     massivews.Stocks,
		MaxRetries: &maxRetries,
	}

	client, err := massivews.New(clientCfg)
	if err != nil {
		logger.Error().Err(err).Msg("could not create massive websocket client")

		runSummary.Status = data.RunFailed

		return
	}
	defer client.Close()

	if err := client.Connect(); err != nil {
		logger.Error().Err(err).Msg("could not connect to massive websocket")

		runSummary.Status = data.RunFailed

		return
	}

	if err := client.Subscribe(massivews.StocksMinAggs); err != nil {
		logger.Error().Err(err).Msg("could not subscribe to AM.* topic")

		runSummary.Status = data.RunFailed

		return
	}

	// Massive's client lifecycle is driven by an internal tomb, not a
	// context. Bridge our session context to its Close() so the
	// deadline (or a parent cancel) terminates the read loop by way of
	// the output channel closing.
	go func() {
		<-sessionCtx.Done()
		client.Close()
	}()

	logger.Info().
		Time("deadline", deadline).
		Str("feed", string(feed)).
		Int("scope_tickers", universe.TickerCount()).
		Msg("massive live 1-minute session starting")

	n, unknown := readMassiveLiveStream(sessionCtx, logger, client, universe, sub, out)

	numObs += n

	logUnknownTickers(logger, time.Now().In(time.UTC).Format("2006-01-02"), "minute_aggs_live", n, unknown)

	switch {
	case sessionCtx.Err() == context.DeadlineExceeded:
		logger.Info().Int("observations", n).Msg("massive live 1-minute session ended at deadline")
	case sessionCtx.Err() == context.Canceled:
		logger.Info().Int("observations", n).Msg("massive live 1-minute session cancelled")
	default:
		// readMassiveLiveStream returns when the output channel closes
		// or an error fires. Neither path matches our planned exit, so
		// treat as a failure.
		logger.Error().Int("observations", n).Msg("massive live 1-minute session ended unexpectedly")

		runSummary.Status = data.RunFailed
	}
}

// readMassiveLiveStream drains the websocket client's Output and
// Error channels until the session context fires, the client closes
// its output channel, or a fatal error arrives. Returns the number
// of bars emitted and an unknown-ticker tally for end-of-session
// reporting.
func readMassiveLiveStream(ctx context.Context, logger *zerolog.Logger, client *massivews.Client, universe *data.AssetHistory, sub *library.Subscription, out chan<- *data.Observation) (int, map[string]int) {
	n := 0
	unknown := map[string]int{}

	for {
		select {
		case err := <-client.Error():
			if err != nil {
				logger.Error().Err(err).Msg("massive websocket client error")
			}

			return n, unknown
		case raw, more := <-client.Output():
			if !more {
				return n, unknown
			}

			agg, ok := raw.(models.EquityAgg)
			if !ok {
				continue
			}

			ticker := massiveTicker2PvTicker(agg.Symbol)
			eventTime := time.UnixMilli(agg.StartTimestamp).UTC()

			figi, ok := universe.FIGIAt(ticker, eventTime)
			if !ok {
				unknown[ticker]++
				continue
			}

			bar := &data.IntradayBar{
				Date:          eventTime,
				Ticker:        ticker,
				CompositeFigi: figi,
				Open:          agg.Open,
				High:          agg.High,
				Low:           agg.Low,
				Close:         agg.Close,
				Volume:        agg.Volume,
			}

			select {
			case <-ctx.Done():
				return n, unknown
			case out <- &data.Observation{
				IntradayBar:      bar,
				ObservationDate:  time.Now(),
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}:
				n++
			}
		}
	}
}

// liveSessionDeadline returns the 20:35 America/New_York wall-clock
// instant on the same date as now (in NYC). Returning an absolute
// instant lets the caller defend against DST transitions and clock
// skew with a simple Time comparison.
func liveSessionDeadline(now time.Time) (time.Time, error) {
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, fmt.Errorf("could not load NYC timezone: %w", err)
	}

	local := now.In(nyc)

	return time.Date(local.Year(), local.Month(), local.Day(), liveSessionEndHourET, liveSessionEndMinuteET, 0, 0, nyc), nil
}

// resolveLiveFeed maps the subscription config string to a
// massivews.Feed value. An empty value defaults to real-time.
func resolveLiveFeed(s string) (massivews.Feed, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", liveFeedRealTime:
		return massivews.RealTime, nil
	case liveFeedDelayed:
		return massivews.Delayed, nil
	default:
		return "", fmt.Errorf("unknown feed %q", s)
	}
}
