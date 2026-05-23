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
	"strings"
	"time"

	"github.com/adrg/strutil"
	"github.com/adrg/strutil/metrics"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// assetViewGenerator emits an explicit column list for the assets published
// view. Required because a SELECT * UNION ALL across asset source tables
// aligns columns positionally and physical column order varies by table
// creation history; listing columns by name makes physical order
// irrelevant. Column order here defines the resulting view's column order;
// keep it stable so downstream consumers do not break.
type assetViewGenerator struct{}

func (assetViewGenerator) SelectFrom(tableName string) string {
	return "SELECT ticker, composite_figi, share_class_figi, primary_exchange, " +
		"asset_type, active, name, description, corporate_url, sector, industry, " +
		"sic_code, cik, cusips, isins, other_identifiers, similar_tickers, tags, " +
		"listed, delisted, last_updated, icon_url, logo_url, search, " +
		"organization_permid, instrument_permid FROM " + tableName
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
	OrganizationPermID   string    `json:"organization_permid" parquet:"name=organization_permid, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY" db:"organization_permid"`
	InstrumentPermID     string    `json:"instrument_permid" parquet:"name=instrument_permid, type=BYTE_ARRAY, convertedtype=UTF8, encoding=PLAIN_DICTIONARY" db:"instrument_permid"`
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
		coalesce(organization_permid, '') as organization_permid,
		coalesce(instrument_permid, '') as instrument_permid,
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
		coalesce(organization_permid, '') as organization_permid,
		coalesce(instrument_permid, '') as instrument_permid,
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

// AssetIndex is a multi-identifier index over previously-persisted
// assets. Lookup tries every available identifier in turn (most
// specific first: CompositeFigi → ShareClassFigi → InstrumentPermID →
// CUSIP → ISIN → Ticker:CIK → Ticker:OrganizationPermID) so a single
// incoming asset can find a DB match through any of its known fields.
// Ticker-alone lookups are deliberately not offered — ticker reuse
// makes them unsafe.
type AssetIndex struct {
	byCompositeFigi    map[string]*Asset
	byShareClassFigi   map[string]*Asset
	byInstrumentPermID map[string]*Asset
	byCUSIP            map[string]*Asset
	byISIN             map[string]*Asset

	// byTickerCIK and byTickerOrgPermID are 1:many. Multiple
	// lifecycles can share the same (ticker, CIK) pair — for
	// example Delta Air Lines kept CIK 0000027904 across its 2005
	// Chapter 11 bankruptcy and 2007 emergence. Lookup picks the
	// candidate whose listed/delisted window contains the asset's
	// ValidFor.
	byTickerCIK       map[string][]*Asset
	byTickerOrgPermID map[string][]*Asset

	// byTicker carries every indexed asset under its ticker as a
	// list (because ticker reuse can produce multiple entries). It
	// is consulted only as a last-resort match for the
	// ticker+name case (e.g. Tiingo mutual funds like POAGX which
	// have neither FIGI nor CIK nor ISIN/CUSIP). Lookup runs a
	// name-similarity gate before returning a match.
	byTicker map[string][]*Asset
}

// BuildAssetIndex constructs an AssetIndex from a flat slice. Each
// asset is written into every per-identifier map for which it has a
// non-empty value. Rows without composite_figi are excluded so callers
// can rely on match.CompositeFigi being usable. When two rows compete
// for the same key, the active row wins; among equally-active rows,
// the most recently updated wins.
func BuildAssetIndex(assets []*Asset) AssetIndex {
	idx := AssetIndex{
		byCompositeFigi:    make(map[string]*Asset, len(assets)),
		byShareClassFigi:   make(map[string]*Asset, len(assets)),
		byInstrumentPermID: make(map[string]*Asset),
		byCUSIP:            make(map[string]*Asset),
		byISIN:             make(map[string]*Asset),
		byTickerCIK:        make(map[string][]*Asset),
		byTickerOrgPermID:  make(map[string][]*Asset),
		byTicker:           make(map[string][]*Asset, len(assets)),
	}

	for _, a := range assets {
		if a == nil || a.Ticker == "" || a.CompositeFigi == "" {
			continue
		}

		assetIndexUpsert(idx.byCompositeFigi, a.CompositeFigi, a)

		if a.ShareClassFigi != "" {
			assetIndexUpsert(idx.byShareClassFigi, a.ShareClassFigi, a)
		}

		if a.InstrumentPermID != "" {
			assetIndexUpsert(idx.byInstrumentPermID, a.InstrumentPermID, a)
		}

		for _, c := range a.CUSIP {
			if c == "" {
				continue
			}

			assetIndexUpsert(idx.byCUSIP, c, a)
		}

		for _, i := range a.ISIN {
			if i == "" {
				continue
			}

			assetIndexUpsert(idx.byISIN, i, a)
		}

		if a.CIK != "" {
			key := a.Ticker + ":" + a.CIK
			idx.byTickerCIK[key] = append(idx.byTickerCIK[key], a)
		}

		if a.OrganizationPermID != "" {
			key := a.Ticker + ":" + a.OrganizationPermID
			idx.byTickerOrgPermID[key] = append(idx.byTickerOrgPermID[key], a)
		}

		idx.byTicker[a.Ticker] = append(idx.byTicker[a.Ticker], a)
	}

	return idx
}

// assetIndexUpsert writes a under key, preferring active over inactive
// and (within the same active state) the most recently updated row.
func assetIndexUpsert(m map[string]*Asset, key string, a *Asset) {
	cur, ok := m[key]
	if !ok {
		m[key] = a
		return
	}

	if cur.Active != a.Active {
		if a.Active {
			m[key] = a
		}

		return
	}

	if a.LastUpdated.After(cur.LastUpdated) {
		m[key] = a
	}
}

// pickLifecycleMatch returns the candidate whose listed/delisted
// window contains asOf. With a single candidate the lookup is
// unambiguous. With multiple, ValidFor disambiguates; when more than
// one candidate's window contains asOf the tiebreaker prefers an
// active row over inactive and, among same active-state rows, the
// most-recently-updated one. Returns (nil, false) when no candidate's
// window contains asOf.
func pickLifecycleMatch(candidates []*Asset, asOf time.Time) (*Asset, bool) {
	if len(candidates) == 0 {
		return nil, false
	}

	if len(candidates) == 1 {
		return candidates[0], true
	}

	if asOf.IsZero() {
		asOf = time.Now()
	}

	var best *Asset

	for _, c := range candidates {
		if !assetLifecycleContains(c, asOf) {
			continue
		}

		if best == nil || lifecycleCandidateBeats(c, best) {
			best = c
		}
	}

	if best == nil {
		return nil, false
	}

	return best, true
}

// lifecycleCandidateBeats reports whether a should be preferred over b
// among candidates whose windows both contain the lookup asOf.
func lifecycleCandidateBeats(a, b *Asset) bool {
	if a.Active != b.Active {
		return a.Active
	}

	return a.LastUpdated.After(b.LastUpdated)
}

// assetLifecycleContains reports whether t falls inside a's listed /
// delisted window. An empty ListingDate is treated as "always listed";
// an empty DelistingDate is treated as "still active".
func assetLifecycleContains(a *Asset, t time.Time) bool {
	if listed := parseAssetDate(a.ListingDate); !listed.IsZero() && t.Before(listed) {
		return false
	}

	if delisted := parseAssetDate(a.DelistingDate); !delisted.IsZero() && t.After(delisted) {
		return false
	}

	return true
}

// Lookup finds the best match in the index for an incoming asset.
// Tries every identifier carried on the asset, in order from most
// specific (security-level) to least specific (entity-level
// disambiguated by ticker). Returns (nil, false) when no identifier
// on the incoming asset matches anything in the index.
func (idx AssetIndex) Lookup(a *Asset) (*Asset, bool) {
	if a == nil {
		return nil, false
	}

	if a.CompositeFigi != "" {
		if m, ok := idx.byCompositeFigi[a.CompositeFigi]; ok {
			return m, true
		}
	}

	if a.ShareClassFigi != "" {
		if m, ok := idx.byShareClassFigi[a.ShareClassFigi]; ok {
			return m, true
		}
	}

	if a.InstrumentPermID != "" {
		if m, ok := idx.byInstrumentPermID[a.InstrumentPermID]; ok {
			return m, true
		}
	}

	for _, c := range a.CUSIP {
		if c == "" {
			continue
		}

		if m, ok := idx.byCUSIP[c]; ok {
			return m, true
		}
	}

	for _, i := range a.ISIN {
		if i == "" {
			continue
		}

		if m, ok := idx.byISIN[i]; ok {
			return m, true
		}
	}

	if a.Ticker != "" && a.CIK != "" {
		if m, ok := pickLifecycleMatch(idx.byTickerCIK[a.Ticker+":"+a.CIK], a.ValidFor); ok {
			return m, true
		}
	}

	if a.Ticker != "" && a.OrganizationPermID != "" {
		if m, ok := pickLifecycleMatch(idx.byTickerOrgPermID[a.Ticker+":"+a.OrganizationPermID], a.ValidFor); ok {
			return m, true
		}
	}

	// Ticker + active state. "Only one active ticker per entity at
	// a time" is a present-tense property: it lets us treat ticker
	// alone as unambiguous only when both sides are currently
	// active. Three conditions must hold:
	//
	//   - a.Active is true (the incoming asset claims to be
	//     currently listed),
	//   - a.ValidFor is recent (a backfill observation with an
	//     old ValidFor may set Active relative to that past date,
	//     not today, so the present-tense property does not apply),
	//   - the index holds exactly one active row for this ticker.
	//
	// Any of those failing falls through to the name-similarity
	// path. Examples that should not match here: BBI as
	// Blockbuster (delisted) showing up against BBI as Brickell
	// (also delisted), or a 2006 backfill observation of a ticker
	// that has since been reassigned to a different entity.
	if a.Ticker != "" && a.Active && isCurrentObservation(a.ValidFor) {
		var (
			activeMatch *Asset
			activeCount int
		)

		for _, m := range idx.byTicker[a.Ticker] {
			if m.Active {
				activeMatch = m
				activeCount++
			}
		}

		if activeCount == 1 {
			return activeMatch, true
		}
	}

	// Last resort: ticker + name with a similarity gate. Useful for
	// providers that emit only ticker and name AND where the DB
	// row in question is inactive (e.g. a historical backfill
	// resurfacing a since-delisted entity). The name gate prevents
	// a same-ticker reuse case from silently inheriting another
	// entity's row.
	if a.Ticker != "" && a.Name != "" {
		for _, m := range idx.byTicker[a.Ticker] {
			if m.Name != "" && assetIndexNamesMatch(a.Name, m.Name) {
				return m, true
			}
		}
	}

	return nil, false
}

// assetIndexNameMatchThreshold is the Jaro-Winkler similarity floor
// for accepting a ticker+name fallback match. 0.85 matches the
// threshold used elsewhere in the codebase for the same purpose
// (provider.JaroWinklerThreshold) so the behaviour is consistent
// even though the helpers live in different packages to avoid an
// import cycle.
const assetIndexNameMatchThreshold = 0.85

// currentObservationWindow is how far back ValidFor can be before
// an observation stops counting as "current" for the ticker+active
// short-circuit in Lookup. Tight enough to exclude any meaningful
// backfill, generous enough to accept a slightly stale snapshot
// (e.g. a yesterday-on-the-wire run).
const currentObservationWindow = 7 * 24 * time.Hour

// isCurrentObservation returns true when t represents "now". Zero
// is treated as the current moment to match SaveDB's convention
// elsewhere in this file (an unset ValidFor means "use now"). A
// non-zero ValidFor must be within currentObservationWindow of the
// present.
func isCurrentObservation(t time.Time) bool {
	if t.IsZero() {
		return true
	}

	return time.Since(t) <= currentObservationWindow
}

// assetIndexNamesMatch returns true when the two names are similar
// enough (case-insensitive Jaro-Winkler ≥ 0.85) to treat as the
// same entity. Empty inputs always return false.
func assetIndexNamesMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	return strutil.Similarity(
		strings.ToLower(a),
		strings.ToLower(b),
		metrics.NewJaroWinkler(),
	) >= assetIndexNameMatchThreshold
}

// Len returns the number of distinct assets indexed (counted by
// CompositeFigi, which every indexed row has). Useful for run-start
// telemetry.
func (idx AssetIndex) Len() int {
	return len(idx.byCompositeFigi)
}

// IsZero returns true when this AssetIndex has not been initialized
// (zero value, no maps allocated). Callers use this to know whether
// to skip the existing-assets step.
func (idx AssetIndex) IsZero() bool {
	return idx.byCompositeFigi == nil
}

type assetIndexCtxKey struct{}

// WithAssetIndex attaches an AssetIndex to ctx so figi.Enrich (and
// any other context-aware callers) can reuse the same pre-built
// index across a run. Returns the original ctx unchanged when idx
// is the zero value (uninitialized).
func WithAssetIndex(ctx context.Context, idx AssetIndex) context.Context {
	if idx.IsZero() {
		return ctx
	}

	return context.WithValue(ctx, assetIndexCtxKey{}, idx)
}

// AssetIndexFromContext returns the AssetIndex attached to ctx via
// WithAssetIndex, or the zero value when none is set. Callers
// should check idx.IsZero() (or rely on Lookup returning false) to
// skip the existing-assets step.
func AssetIndexFromContext(ctx context.Context) AssetIndex {
	if v := ctx.Value(assetIndexCtxKey{}); v != nil {
		if idx, ok := v.(AssetIndex); ok {
			return idx
		}
	}

	return AssetIndex{}
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

	// $26 (valid_for) is not persisted as a column. It guards the
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
		"organization_permid",
		"instrument_permid",
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
		$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
		$24, $25
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		primary_exchange = EXCLUDED.primary_exchange,
		active = CASE
			WHEN %[1]s.delisted IS NOT NULL AND $26 < %[1]s.delisted THEN %[1]s.active
			ELSE EXCLUDED.active
		END,
		name = EXCLUDED.name,
		description = EXCLUDED.description,
		corporate_url = EXCLUDED.corporate_url,
		sector = EXCLUDED.sector,
		industry = EXCLUDED.industry,
		sic_code = EXCLUDED.sic_code,
		cik = EXCLUDED.cik,
		organization_permid = COALESCE(NULLIF(EXCLUDED.organization_permid, ''), %[1]s.organization_permid),
		instrument_permid = COALESCE(NULLIF(EXCLUDED.instrument_permid, ''), %[1]s.instrument_permid),
		cusips = EXCLUDED.cusips,
		isins = EXCLUDED.isins,
		other_identifiers = EXCLUDED.other_identifiers,
		similar_tickers = EXCLUDED.similar_tickers,
		tags = EXCLUDED.tags,
		listed = EXCLUDED.listed,
		delisted = CASE
			WHEN %[1]s.delisted IS NOT NULL AND $26 < %[1]s.delisted THEN %[1]s.delisted
			ELSE EXCLUDED.delisted
		END,
		last_updated = EXCLUDED.last_updated,
		icon_url = COALESCE(EXCLUDED.icon_url, %[1]s.icon_url),
		logo_url = COALESCE(EXCLUDED.logo_url, %[1]s.logo_url)`, tbl)

	_, err = tx.Exec(ctx, sql, asset.Ticker, asset.CompositeFigi, asset.ShareClassFigi,
		asset.PrimaryExchange, asset.AssetType, asset.Active, asset.Name, asset.Description,
		asset.CorporateUrl, asset.Sector, asset.Industry, asset.SIC, asset.CIK,
		asset.OrganizationPermID, asset.InstrumentPermID,
		filterEmpty(asset.CUSIP), filterEmpty(asset.ISIN),
		asset.OtherIdentifiers, asset.SimilarTickers, asset.Tags,
		listingDate, delistingDate, asset.LastUpdated,
		brandingBind(asset.IconUrl), brandingBind(asset.LogoUrl), validFor)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save asset to DB failed")
		return err
	}

	return nil
}

// filterEmpty returns a copy of in with empty / whitespace-only entries
// removed. Used to prevent malformed slices like `{""}` from landing in
// the CUSIP / ISIN text[] columns; one bad provider row should not
// pollute the canonical identifier list. Returns nil rather than an
// empty slice when all entries are dropped so the column stores NULL.
func filterEmpty(in []string) []string {
	out := in[:0:0]

	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
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
