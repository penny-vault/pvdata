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
package provider

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

type YFinanceStreamer struct{}

func (yfinance *YFinanceStreamer) Name() string {
	return "YFinanceStreamer"
}

func (yfinance *YFinanceStreamer) ConfigDescription() map[string]string {
	return map[string]string{
		"tickers": "Enter a comma seperated list of tickers to stream in the form Yahoo Ticker:Ticker:Composite FIGI (e.g. ^GSPC:SPX:BBG000H4FSM0):",
	}
}

func (yfinance *YFinanceStreamer) Description() string {
	return `YFinanceStreamer publishes a real-time stream of quote data during market hours`
}

func (yfinance *YFinanceStreamer) Datasets() map[string]Dataset {
	now := time.Now().UTC()
	return map[string]Dataset{
		"Real-time Quotes": {
			Name:        "Real-time Quotes",
			Description: "Real-time streaming stock quotes from Yahoo Finance",
			DataTypes:   []*data.DataType{data.DataTypes[data.QuoteKey]},
			DateRange: func() (time.Time, time.Time) {
				return now, now
			},
			Fetch: yfinanceStreamer,
		},
	}
}

func yfinanceStreamer(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs
		runSummary.Status = data.RunSuccess
		exitNotification <- runSummary
	}()
}
