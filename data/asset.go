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
	"errors"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// assetViewGenerator emits an explicit column list for the assets published
// view. Required because a SELECT * UNION ALL across asset source tables
// aligns columns positionally, and the search tsvector lands in different
// physical positions depending on whether the table was created fresh at
// schema v1 (search appended last) or migrated from v0 (search appended
// before icon_url/logo_url, which are then added by the migration). Listing
// columns by name makes physical order irrelevant.
//
// Column order here defines the resulting view's column order; keep it
// stable so downstream consumers do not break.
type assetViewGenerator struct{}

func (assetViewGenerator) SelectFrom(tableName string) string {
	return "SELECT ticker, composite_figi, share_class_figi, primary_exchange, " +
		"asset_type, active, name, description, corporate_url, sector, industry, " +
		"sic_code, cik, cusips, isins, other_identifiers, similar_tickers, tags, " +
		"listed, delisted, last_updated, icon_url, logo_url, search FROM " + tableName
}

type AssetType string

const (
	CommonStock  AssetType = "CS"
	ETF          AssetType = "ETF"
	ETN          AssetType = "ETN"
	CEF          AssetType = "CEF"
	MutualFund   AssetType = "MF"
	ADRC         AssetType = "ADRC"
	FRED         AssetType = "FRED"
	SYNTH        AssetType = "SYNTH"
	INDEX        AssetType = "INDEX"
	UnknownAsset AssetType = "Unknown"
)

type Exchange string

const (
	NasdaqExchange  Exchange = "XNAS"
	NYSEExchange    Exchange = "XNYS"
	BATSExchange    Exchange = "BATS"
	NYSEMktExchange Exchange = "XASE"
	NMFQSExchange   Exchange = "NMFQS"
	ARCAExchange    Exchange = "ARCX"
	IndexExchange   Exchange = "INDEX"
	OTCExchange     Exchange = "OTC"
	UnknownExchange Exchange = "UNK"
)

type Asset struct {
	Ticker               string    `json:"ticker" parquet:"name=ticker, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	Name                 string    `json:"name" parquet:"name=name, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	Description          string    `json:"description" parquet:"name=description, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	PrimaryExchange      Exchange  `json:"primary_exchange" toml:"primary_exchange" parquet:"name=primary_exchange, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	AssetType            AssetType `json:"asset_type" toml:"asset_type" parquet:"name=asset_type, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	CompositeFigi        string    `json:"composite_figi" toml:"composite_figi" parquet:"name=composite_figi, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	ShareClassFigi       string    `json:"share_class_figi" toml:"share_class_figi" parquet:"name=share_class_figi, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	Active               bool      `json:"active" toml:"active" parquet:"name=active, type=BOOLEAN"`
	CUSIP                []string  `json:"cusips" parquet:"name=cusip, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY" db:"cusips"`
	ISIN                 []string  `json:"isins" parquet:"name=isin, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY" db:"isins"`
	CIK                  string    `json:"cik" parquet:"name=cik, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	SIC                  *int      `json:"sic" db:"sic_code"`
	ListingDate          string    `json:"listing_date" toml:"listing_date" parquet:"name=listing_date, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY" db:"listed"`
	DelistingDate        string    `json:"delisting_date" toml:"delisting_date" parquet:"name=delisting_date, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY" db:"delisted"`
	Industry             string    `json:"industry" parquet:"name=industry, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	Sector               string    `json:"sector" parquet:"name=sector, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	Icon                 []byte    `parquet:"name=icon, type=BYTE_ARRAY"`
	IconMimeType         string    `parquet:"name=icon_mime_type, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	Logo                 []byte    `parquet:"name=logo, type=BYTE_ARRAY"`
	LogoMimeType         string    `parquet:"name=logo_mime_type, tyle=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	IconUrl              string    `json:"icon_url" db:"icon_url"`
	LogoUrl              string    `json:"logo_url" db:"logo_url"`
	CorporateUrl         string    `json:"corporate_url" toml:"corporate_url" parquet:"name=corporate_url, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	HeadquartersLocation string    `json:"headquarters_location" toml:"headquarters_location" parquet:"name=headquarters_location, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY"`
	OtherIdentifiers     map[string]string
	Tags                 []string
	SimilarTickers       []string  `json:"similar_tickers" toml:"similar_tickers" parquet:"name=similar_tickers, type=MAP, convertedtype=LIST, valuetype=BYTE_ARRAY, valueconvertedtype=UTF8"`
	LastUpdated          time.Time `json:"last_updated" parquet:"name=last_updated, type=INT64"`
	// ValidFor is the as-of date of the source observation. SaveDB
	// uses it to guard active/delisted updates: an observation whose
	// ValidFor predates the stored delisted timestamp will not flip
	// those lifecycle fields. Zero value is treated as "now" by SaveDB.
	ValidFor time.Time `json:"-" db:"-"`
}

func ActiveAssets(ctx context.Context, dbConn *pgxpool.Conn, tables ...string) ([]*Asset, error) {
	var assetTable string
	if len(tables) == 0 {
		assetTable = "assets"
	} else {
		assetTable = tables[0]
	}

	sql := fmt.Sprintf(`SELECT
		ticker,
		composite_figi,
		share_class_figi,
		primary_exchange,
		asset_type,
		active,
		name,
		description,
		corporate_url,
		sector,
		industry,
		sic_code,
		cik,
		cusips,
		isins,
		other_identifiers,
		similar_tickers,
		tags,
		coalesce(to_char(listed, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as listed,
		coalesce(to_char(delisted, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as delisted,
		last_updated,
		coalesce(icon_url, '') as icon_url,
		coalesce(logo_url, '') as logo_url
	FROM %s
	WHERE active=true`, assetTable)

	rows, err := dbConn.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("query active assets from %s: %w", assetTable, err)
	}

	var dbActiveAssets []*Asset

	err = pgxscan.ScanAll(&dbActiveAssets, rows)
	if err != nil {
		return nil, fmt.Errorf("scan active assets: %w", err)
	}

	return dbActiveAssets, nil
}

func AllAssets(ctx context.Context, dbConn *pgxpool.Conn, tables ...string) ([]*Asset, error) {
	var assetTable string
	if len(tables) == 0 {
		assetTable = "assets"
	} else {
		assetTable = tables[0]
	}

	sql := fmt.Sprintf(`SELECT
		coalesce(ticker, '') as ticker,
		coalesce(composite_figi, '') as composite_figi,
		coalesce(share_class_figi, '') as share_class_figi,
		coalesce(primary_exchange, '') as primary_exchange,
		coalesce(asset_type::text, '') as asset_type,
		coalesce(active, false) as active,
		coalesce(name, '') as name,
		coalesce(description, '') as description,
		coalesce(corporate_url, '') as corporate_url,
		coalesce(sector, '') as sector,
		coalesce(industry, '') as industry,
		sic_code,
		coalesce(cik, '') as cik,
		cusips,
		isins,
		other_identifiers,
		similar_tickers,
		tags,
		coalesce(to_char(listed, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as listed,
		coalesce(to_char(delisted, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as delisted,
		coalesce(last_updated, '0001-01-01'::timestamp) as last_updated,
		coalesce(icon_url, '') as icon_url,
		coalesce(logo_url, '') as logo_url
	FROM %s`, assetTable)

	rows, err := dbConn.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("query all assets from %s: %w", assetTable, err)
	}

	var dbAllAssets []*Asset

	err = pgxscan.ScanAll(&dbAllAssets, rows)
	if err != nil {
		return nil, fmt.Errorf("scan all assets: %w", err)
	}

	return dbAllAssets, nil
}

func (asset *Asset) ID() string {
	return fmt.Sprintf("%s:%s", asset.Ticker, asset.CompositeFigi)
}

func (asset *Asset) SaveFiles(ctx context.Context, filer Filer) error {
	if url, err := saveAssetFile(filer, asset.CompositeFigi+"-icon", asset.IconMimeType, asset.Icon); err != nil {
		log.Error().Err(err).Str("Name", asset.CompositeFigi+"-icon").Msg("error saving icon")
	} else if url != "" {
		asset.IconUrl = url
	}

	if url, err := saveAssetFile(filer, asset.CompositeFigi+"-logo", asset.LogoMimeType, asset.Logo); err != nil {
		log.Error().Err(err).Str("Name", asset.CompositeFigi+"-logo").Msg("error saving logo")
	} else if url != "" {
		asset.LogoUrl = url
	}

	return nil
}

// saveAssetFile dispatches by mime type and writes the bytes via
// filer. Returns the URL/path Filer.CreateFile reports, or "" when
// there is nothing to save (empty mime type or zero-byte payload).
func saveAssetFile(filer Filer, baseName, mimeType string, data []byte) (string, error) {
	if len(data) == 0 || mimeType == "" {
		return "", nil
	}

	var ext string

	switch mimeType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/svg+xml", "image/svg":
		ext = ".svg"
	default:
		return "", errors.New("unknown mimetype: " + mimeType)
	}

	return filer.CreateFile(baseName+ext, data)
}

func (asset *Asset) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if asset.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing asset transaction to database")
		}
	}()

	listingDate := &asset.ListingDate
	delistingDate := &asset.DelistingDate

	if asset.ListingDate == "" {
		listingDate = nil
	}

	if asset.DelistingDate == "" {
		delistingDate = nil
	}

	validFor := asset.ValidFor
	if validFor.IsZero() {
		validFor = time.Now()
	}

	log.Debug().Object("Asset", asset).Msg("Saving asset to database")

	// $24 (valid_for) is not persisted as a column. It guards the
	// active/delisted lifecycle fields against being overwritten by
	// stale (older as-of-date) observations: when the stored row is
	// already known to be delisted and the incoming observation
	// predates that delisting, both fields keep their existing
	// values. All other fields update normally — newest write wins.
	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"share_class_figi",
		"primary_exchange",
		"asset_type",
		"active",
		"name",
		"description",
		"corporate_url",
		"sector",
		"industry",
		"sic_code",
		"cik",
		"cusips",
		"isins",
		"other_identifiers",
		"similar_tickers",
		"tags",
		"listed",
		"delisted",
		"last_updated",
		"icon_url",
		"logo_url"
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		primary_exchange = EXCLUDED.primary_exchange,
		active = CASE
			WHEN %[1]s.delisted IS NOT NULL AND $24 < %[1]s.delisted THEN %[1]s.active
			ELSE EXCLUDED.active
		END,
		name = EXCLUDED.name,
		description = EXCLUDED.description,
		corporate_url = EXCLUDED.corporate_url,
		sector = EXCLUDED.sector,
		industry = EXCLUDED.industry,
		sic_code = EXCLUDED.sic_code,
		cik = EXCLUDED.cik,
		cusips = EXCLUDED.cusips,
		isins = EXCLUDED.isins,
		other_identifiers = EXCLUDED.other_identifiers,
		similar_tickers = EXCLUDED.similar_tickers,
		tags = EXCLUDED.tags,
		listed = EXCLUDED.listed,
		delisted = CASE
			WHEN %[1]s.delisted IS NOT NULL AND $24 < %[1]s.delisted THEN %[1]s.delisted
			ELSE EXCLUDED.delisted
		END,
		last_updated = EXCLUDED.last_updated,
		icon_url = COALESCE(EXCLUDED.icon_url, %[1]s.icon_url),
		logo_url = COALESCE(EXCLUDED.logo_url, %[1]s.logo_url)`, tbl)

	_, err = tx.Exec(ctx, sql, asset.Ticker, asset.CompositeFigi, asset.ShareClassFigi,
		asset.PrimaryExchange, asset.AssetType, asset.Active, asset.Name, asset.Description,
		asset.CorporateUrl, asset.Sector, asset.Industry, asset.SIC, asset.CIK,
		asset.CUSIP, asset.ISIN, asset.OtherIdentifiers, asset.SimilarTickers, asset.Tags,
		listingDate, delistingDate, asset.LastUpdated,
		brandingBind(asset.IconUrl), brandingBind(asset.LogoUrl), validFor)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save asset to DB failed")
		return err
	}

	return nil
}

func (asset *Asset) MarshalZerologObject(e *zerolog.Event) {
	e.Str("Ticker", asset.Ticker)
	e.Str("Name", asset.Name)
	e.Str("Description", asset.Description)
	e.Str("PrimaryExchange", string(asset.PrimaryExchange))
	e.Str("AssetType", string(asset.AssetType))
	e.Str("CompositeFigi", asset.CompositeFigi)
	e.Str("ShareClassFigi", asset.ShareClassFigi)
	e.Bool("Active", asset.Active)
	e.Strs("CUSIP", asset.CUSIP)
	e.Strs("ISIN", asset.ISIN)
	e.Str("CIK", asset.CIK)

	if asset.SIC != nil {
		e.Int("SIC", *asset.SIC)
	}

	e.Str("ListingDate", asset.ListingDate)
	e.Str("DelistingDate", asset.DelistingDate)
	e.Str("Industry", asset.Industry)
	e.Str("Sector", asset.Sector)
	e.Str("CorporateURL", asset.CorporateUrl)
	e.Str("HeadquartersLocation", asset.HeadquartersLocation)
	e.Str("IconUrl", asset.IconUrl)
	e.Str("LogoUrl", asset.LogoUrl)

	for key, val := range asset.OtherIdentifiers {
		e.Str(key, val)
	}

	e.Strs("Tags", asset.Tags)
	e.Strs("SimilarTickers", asset.SimilarTickers)
	e.Time("LastUpdated", asset.LastUpdated)
}

// brandingBind converts an empty IconUrl/LogoUrl to a SQL NULL so
// the missing-branding lane (which queries WHERE icon_url IS NULL)
// can find assets that haven't been uploaded yet. Non-empty values
// are passed through unchanged.
func brandingBind(url string) any {
	if url == "" {
		return nil
	}

	return url
}
