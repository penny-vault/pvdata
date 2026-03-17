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

// PostFetchHook is a function that runs after a dataset fetch completes successfully.
type PostFetchHook func(context.Context, *library.Subscription) error

type contextKey string

const LookbackKey contextKey = "lookback"

// LookbackFromContext returns the lookback duration from the context,
// falling back to the given default if not set.
func LookbackFromContext(ctx context.Context, defaultLookback time.Duration) time.Duration {
	if v := ctx.Value(LookbackKey); v != nil {
		if d, ok := v.(time.Duration); ok {
			return d
		}
	}

	return defaultLookback
}

type Provider interface {
	Name() string
	ConfigDescription() map[string]string
	Description() string
	Datasets() map[string]Dataset
}

// FileImporter is an optional interface that providers can implement to support
// importing data from local files (parquet, CSV, etc.) instead of fetching from APIs.
type FileImporter interface {
	ImportFiles(ctx context.Context, sub *library.Subscription,
		files []string, out chan<- *data.Observation, exit chan<- data.RunSummary)
}

type Dataset struct {
	Name        string
	Description string
	DataTypes   []*data.DataType
	DateRange   func() (time.Time, time.Time)
	PostFetch   []PostFetchHook
	TTL         time.Duration

	// Fetch is called when pvdata wants to retrieve measurements from the dataset. It
	// passes a config with the provider configuration, a channel to write results to,
	// a logger to write log messages to, and a channel to write progress.
	Fetch func(context.Context, *library.Subscription, chan<- *data.Observation, chan<- data.RunSummary)
}
