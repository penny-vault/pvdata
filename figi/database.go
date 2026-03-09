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
package figi

import (
	"context"

	"github.com/alphadose/haxmap"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

var (
	figiMap *haxmap.Map[string, string]
)

func init() {
	figiMap = haxmap.New[string, string]()
}

func MapInstance() *haxmap.Map[string, string] {
	return figiMap
}

func LoadCacheFromDB(ctx context.Context, dbConn *pgxpool.Conn) {
	sql := "SELECT ticker, composite_figi FROM assets WHERE active=true"

	rows, err := dbConn.Query(ctx, sql)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save asset to DB failed")
		return
	}

	var dbActiveAssets []*data.Asset

	err = pgxscan.ScanAll(&dbActiveAssets, rows)
	if err != nil {
		log.Error().Err(err).Msg("error when scanning values into dbActiveAssets")
	}

	// make sure figiMap is initialized
	figiMap := MapInstance()

	for _, asset := range dbActiveAssets {
		figiMap.Set(asset.Ticker, asset.CompositeFigi)
	}
}
