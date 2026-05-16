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
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/go-resty/resty/v2"
	"github.com/goccy/go-json"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

const (
	flatFilesEndpoint = "https://files.massive.com"
	flatFilesBucket   = "flatfiles"
	dayAggsKeyFormat  = "us_stocks_sip/day_aggs_v1/%04d/%02d/%s.csv.gz"

	defaultEODLookback = 7 * 24 * time.Hour
	defaultRateLimit   = 600
)

// REST endpoint URLs are vars (not const) so that httptest-backed
// tests can swap them to a local server. Production code never
// reassigns these.
var (
	splitsURL    = "https://api.massive.com/v3/reference/splits"
	dividendsURL = "https://api.massive.com/v3/reference/dividends"
)

func downloadMassiveEOD(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
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

	accessKey := strings.TrimSpace(sub.Config["flatFilesAccessKey"])
	secretKey := strings.TrimSpace(sub.Config["flatFilesSecretKey"])

	if accessKey == "" || secretKey == "" {
		logger.Error().Msg("massive EOD requires flatFilesAccessKey and flatFilesSecretKey")

		runSummary.Status = data.RunFailed

		return
	}

	rateLimit := readEODRateLimit(sub.Config["rateLimit"])
	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/61.0), 1)
	restClient := newMassiveRESTClient(sub.Config["apiKey"])
	s3Client := newFlatFilesClient(accessKey, secretKey)

	end := time.Now().UTC().Truncate(24 * time.Hour)
	lookback := provider.LookbackFromContext(ctx, defaultEODLookback)
	start := end.Add(-lookback).Truncate(24 * time.Hour)

	conn, err := sub.Library.AcquireWithTimeout(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("could not acquire database connection")

		runSummary.Status = data.RunFailed

		return
	}

	// Asset universe always reads from the published "assets" view -
	// it is the canonical union across every asset-producing
	// subscription. Reading from a per-subscription table would miss
	// tickers owned by other providers (and breaks for subscriptions
	// like EOD that don't even own AssetKey themselves).
	//
	// AllAssets (not ActiveAssets) is used so historical bars for
	// since-delisted tickers still resolve. The AssetHistory index
	// gates each row against the asset's listed/delisted dates, so
	// today's `active=false` does not drop yesterday's data.
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
		logger.Warn().Msg("no assets in scope for massive EOD; skipping run")
		return
	}

	logger.Info().
		Time("start", start).
		Time("end", end).
		Int("scope_tickers", universe.TickerCount()).
		Msg("massive flat-files EOD loader starting")

	logger.Info().Time("from", start).Time("to", end).Msg("fetching splits from massive REST")

	splits, allSplits, err := fetchSplitsRange(ctx, restClient, limiter, start, end)
	if err != nil {
		logger.Error().Err(err).Msg("could not fetch splits from massive REST")

		runSummary.Status = data.RunFailed

		return
	}

	logger.Info().Time("from", start).Time("to", end).Msg("fetching dividends from massive REST")

	divs, allDivs, err := fetchDividendsRange(ctx, restClient, limiter, start, end)
	if err != nil {
		logger.Error().Err(err).Msg("could not fetch dividends from massive REST")

		runSummary.Status = data.RunFailed

		return
	}

	logger.Info().
		Int("split_tickers", splits.tickerCount()).
		Int("dividend_tickers", divs.tickerCount()).
		Int("split_records", len(allSplits)).
		Int("dividend_records", len(allDivs)).
		Msg("loaded corporate actions for window; starting daily flat-file iteration")

	// Faithful per-year parquet backups of the corporate-action API
	// responses. Failures log a warning but do not abort the run -
	// EOD save is the primary contract.
	if backupDir := strings.TrimSpace(viper.GetString("parquet_backup_dir")); backupDir != "" {
		base := filepath.Join(backupDir, subscriptionBackupSlug(sub))

		if err := writeCorporateActionsBackup(filepath.Join(base, "splits"), allSplits, splitYear); err != nil {
			logger.Warn().Err(err).Msg("splits parquet backup failed")
		}

		if err := writeCorporateActionsBackup(filepath.Join(base, "dividends"), allDivs, dividendYear); err != nil {
			logger.Warn().Err(err).Msg("dividends parquet backup failed")
		}
	}

	process := func(ctx context.Context, d time.Time) (int, error) {
		if flatFileAvailableForDate(d, time.Now()) {
			return streamDayAggsForDate(ctx, s3Client, sub, universe, splits, divs, d, out)
		}

		return streamDailyMarketSummaryForDate(ctx, restClient, limiter, sub, universe, splits, divs, d, out)
	}

	workers := pickFlatFileWorkers(logger, start, end, "day_aggs")

	n, err := downloadDailyAggsRange(ctx, logger, "day_aggs", workers, start, end, process)
	if err != nil {
		runSummary.Status = data.RunFailed
		return
	}

	numObs += n
}

// newFlatFilesClient builds an S3 client targeting Massive's
// S3-compatible flat-files endpoint. Path-style addressing is used
// because the endpoint does not implement bucket-as-subdomain.
func newFlatFilesClient(accessKey, secretKey string) *s3.Client {
	return s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(flatFilesEndpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		UsePathStyle: true,
	})
}

// dayAggsKey renders the S3 object key for the day-aggregates flat
// file covering the given UTC date.
func dayAggsKey(d time.Time) string {
	return fmt.Sprintf(dayAggsKeyFormat, d.Year(), int(d.Month()), d.Format("2006-01-02"))
}

func isWeekend(d time.Time) bool {
	return d.Weekday() == time.Saturday || d.Weekday() == time.Sunday
}

// streamDayAggsForDate downloads and parses the day-aggregates file
// for date d. Rows whose ticker had no figi in the universe AS OF
// date d are dropped (i.e. tickers that hadn't listed yet, or had
// already delisted). A NoSuchKey response (non-trading day, or file
// not yet published) is treated as an empty result rather than an
// error. Transient transport / mid-stream failures are retried with
// exponential backoff before giving up on the date.
func streamDayAggsForDate(ctx context.Context, client *s3.Client, sub *library.Subscription, universe *data.AssetHistory, splits, divs corporateActions, d time.Time, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)
	key := dayAggsKey(d)

	rows, err := fetchAndParseAggs(ctx, client, key)
	if err != nil {
		if errors.Is(err, errFlatFileMissing) {
			logger.Debug().Str("key", key).Msg("flat file not present; skipping date")
			return 0, nil
		}

		return 0, err
	}

	// Optional parquet backup of the source file. Backup failures are
	// logged but do not abort the run - the EOD save path is the
	// primary contract; the backup is best-effort archival.
	if backupDir := strings.TrimSpace(viper.GetString("parquet_backup_dir")); backupDir != "" {
		destPath := backupPathFor(filepath.Join(backupDir, subscriptionBackupSlug(sub)), d)

		exists, statErr := backupExists(destPath)
		switch {
		case statErr != nil:
			logger.Warn().Err(statErr).Str("path", destPath).Msg("could not stat backup destination; skipping backup")
		case exists:
			logger.Debug().Str("path", destPath).Msg("parquet backup already present; skipping")
		default:
			if err := writeFlatFileBackup(destPath, rows); err != nil {
				logger.Warn().Err(err).Str("path", destPath).Msg("parquet backup failed")
			} else {
				logger.Info().Str("path", destPath).Int("rows", len(rows)).Msg("wrote parquet backup")
			}
		}
	}

	return emitDayAggRowsAsEod(ctx, sub, universe, splits, divs, rows, d, "day_aggs", out)
}

// emitDayAggRowsAsEod publishes parsed day-aggregate rows as Eod
// observations. Tickers without a figi in the universe AS OF date d
// are dropped (and counted in the per-date unknowns log). The
// `source` label appears in the unknown-ticker summary so operators
// can tell flat-file and REST runs apart.
func emitDayAggRowsAsEod(ctx context.Context, sub *library.Subscription, universe *data.AssetHistory, splits, divs corporateActions, rows []aggRow, d time.Time, source string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	eventTime, err := buildMassiveEodEvent(d)
	if err != nil {
		return 0, err
	}

	dateStr := d.Format("2006-01-02")

	n := 0
	unknown := map[string]int{}

	for _, row := range rows {
		ticker := massiveTicker2PvTicker(row.Ticker)

		figi, ok := universe.FIGIAt(ticker, d)
		if !ok {
			unknown[ticker]++
			continue
		}

		split := splits.lookup(ticker, dateStr)
		if split == 0 {
			split = 1.0
		}

		eod := &data.Eod{
			Date:          eventTime,
			Ticker:        ticker,
			CompositeFigi: figi,
			Open:          row.Open,
			High:          row.High,
			Low:           row.Low,
			Close:         row.Close,
			Volume:        row.Volume,
			Dividend:      divs.lookup(ticker, dateStr),
			Split:         split,
		}

		select {
		case <-ctx.Done():
			return n, ctx.Err()
		case out <- &data.Observation{
			EodQuote:         eod,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}:
			n++
		}
	}

	logUnknownTickers(logger, dateStr, source, n, unknown)

	return n, nil
}

// isNoSuchKey returns true for the various shapes a 404 takes from
// the AWS SDK when the requested object does not exist.
func isNoSuchKey(err error) bool {
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}

	return false
}

// aggRow is a single parsed row from an aggregates flat file. Both
// day_aggs_v1 (one row per ticker per day) and minute_aggs_v1 (one
// row per ticker per minute) share the exact same CSV schema and
// reuse this struct + parser. The parquet struct tags drive the
// column names in the optional backup file written by
// writeFlatFileBackup; window_start carries minute-level resolution
// for minute_aggs and is the day open boundary for day_aggs.
type aggRow struct {
	Ticker       string  `parquet:"ticker"`
	Volume       float64 `parquet:"volume"`
	Open         float64 `parquet:"open"`
	Close        float64 `parquet:"close"`
	High         float64 `parquet:"high"`
	Low          float64 `parquet:"low"`
	WindowStart  int64   `parquet:"window_start"`
	Transactions int64   `parquet:"transactions"`
}

// parseAggs reads CSV rows from r (already gunzipped) and returns
// the parsed rows. The expected header is
// "ticker,volume,open,close,high,low,window_start,transactions" but
// columns are looked up by name so order/extra columns are tolerated.
// window_start and transactions are optional - missing values are zero.
func parseAggs(r io.Reader) ([]aggRow, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	cols := indexHeader(header)

	for _, name := range []string{"ticker", "volume", "open", "close", "high", "low"} {
		if _, ok := cols[name]; !ok {
			return nil, fmt.Errorf("missing column %q", name)
		}
	}

	var rows []aggRow

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return rows, fmt.Errorf("read row: %w", err)
		}

		rows = append(rows, aggRow{
			Ticker:       record[cols["ticker"]],
			Volume:       parseFloat(record[cols["volume"]]),
			Open:         parseFloat(record[cols["open"]]),
			Close:        parseFloat(record[cols["close"]]),
			High:         parseFloat(record[cols["high"]]),
			Low:          parseFloat(record[cols["low"]]),
			WindowStart:  parseInt(getCol(record, cols, "window_start")),
			Transactions: parseInt(getCol(record, cols, "transactions")),
		})
	}

	return rows, nil
}

// getCol returns the field value from record at the named column, or
// the empty string if the column is absent. Used for optional CSV
// columns whose presence is not required by parseAggs.
func getCol(record []string, cols map[string]int, name string) string {
	idx, ok := cols[name]
	if !ok || idx >= len(record) {
		return ""
	}

	return record[idx]
}

func parseInt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}

	return v
}

func indexHeader(header []string) map[string]int {
	idx := make(map[string]int, len(header))

	for i, name := range header {
		idx[strings.ToLower(strings.TrimSpace(name))] = i
	}

	return idx
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}

	return v
}

func buildMassiveEodEvent(d time.Time) (time.Time, error) {
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, fmt.Errorf("could not load NYC timezone: %w", err)
	}

	return time.Date(d.Year(), d.Month(), d.Day(), 16, 0, 0, 0, nyc), nil
}

func readEODRateLimit(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultRateLimit
	}

	return n
}

// corporateActions maps ticker -> ISO date (YYYY-MM-DD) -> value.
// Used for both split factors and per-share dividend cash amounts.
type corporateActions map[string]map[string]float64

func newCorporateActions(capacity int) corporateActions {
	return make(corporateActions, capacity)
}

func (c corporateActions) lookup(ticker, date string) float64 {
	if c == nil {
		return 0
	}

	return c[ticker][date]
}

func (c corporateActions) set(ticker, date string, value float64) {
	if c == nil {
		return
	}

	inner, ok := c[ticker]
	if !ok {
		inner = make(map[string]float64, 4)
		c[ticker] = inner
	}

	inner[date] = value
}

func (c corporateActions) tickerCount() int {
	return len(c)
}

// numFetchWindows is the default number of parallel sub-windows used
// when fetching paginated REST listings (splits, dividends). Polygon's
// cursor-pagination latency grows with cursor depth, so splitting a
// long date range into N independent shallow chains and running them
// concurrently is roughly bounded by max(per-window-time) rather than
// sum(per-window-time). 4 is enough to recover most of the speedup
// without saturating the rate limiter on heavy ranges.
const numFetchWindows = 4

// minDaysForParallel is the lookback threshold below which parallel
// pagination is skipped. Daily and weekly incremental runs typically
// fit in a single 1000-record page, so spawning 4 goroutines and 4
// HTTP connections for them adds overhead and log noise without any
// throughput benefit.
const minDaysForParallel = 365

// fetchWindow is a half-open-on-the-right calendar interval (start
// inclusive, end inclusive in API params; consecutive windows are
// stepped by one day to ensure no overlap on the seam).
type fetchWindow struct {
	start time.Time
	end   time.Time
}

// splitFetchWindows divides [start, end] into n non-overlapping
// non-skipping windows. Window i+1's start is window i's end + 1 day,
// so a query of (gte=window.start, lte=window.end) on each produces
// disjoint sets that together cover the original range exactly once.
// Falls back to a single window when:
//   - n <= 1
//   - start is not before end (degenerate or inverted range)
//   - the range is shorter (in days) than n
func splitFetchWindows(start, end time.Time, n int) []fetchWindow {
	if n <= 1 || !start.Before(end) {
		return []fetchWindow{{start: start, end: end}}
	}

	totalDays := int(end.Sub(start).Hours()/24) + 1
	if totalDays < n || totalDays < minDaysForParallel {
		return []fetchWindow{{start: start, end: end}}
	}

	daysPerWindow := totalDays / n

	out := make([]fetchWindow, 0, n)
	cur := start

	for i := range n {
		var next time.Time
		if i == n-1 {
			next = end
		} else {
			next = cur.AddDate(0, 0, daysPerWindow-1)
		}

		out = append(out, fetchWindow{start: cur, end: next})
		cur = next.AddDate(0, 0, 1)
	}

	return out
}

// splitYear extracts the calendar year from a split's execution_date
// for parquet bucketing. An unparseable date returns zero, which sends
// the record to a 0000.parquet file rather than dropping it.
func splitYear(s massiveSplit) int {
	t, err := time.Parse("2006-01-02", s.ExecutionDate)
	if err != nil {
		return 0
	}

	return t.Year()
}

// dividendYear extracts the calendar year from a dividend's
// ex_dividend_date for parquet bucketing. Same fallback behaviour as
// splitYear.
func dividendYear(d massiveDividend) int {
	t, err := time.Parse("2006-01-02", d.ExDividendDate)
	if err != nil {
		return 0
	}

	return t.Year()
}

// massiveSplit mirrors every field documented for the
// /v3/reference/splits endpoint. Parquet tags are present so the same
// struct doubles as the schema for the per-year backup file.
type massiveSplit struct {
	ID            string  `json:"id" parquet:"id"`
	Ticker        string  `json:"ticker" parquet:"ticker"`
	ExecutionDate string  `json:"execution_date" parquet:"execution_date"`
	SplitFrom     float64 `json:"split_from" parquet:"split_from"`
	SplitTo       float64 `json:"split_to" parquet:"split_to"`
}

// massiveDividend mirrors every field documented for the
// /v3/reference/dividends endpoint. declaration_date is documented but
// is sometimes absent on individual records; JSON unmarshal leaves it
// empty in that case, which the parquet write preserves verbatim.
type massiveDividend struct {
	ID              string  `json:"id" parquet:"id"`
	Ticker          string  `json:"ticker" parquet:"ticker"`
	CashAmount      float64 `json:"cash_amount" parquet:"cash_amount"`
	Currency        string  `json:"currency" parquet:"currency"`
	DividendType    string  `json:"dividend_type" parquet:"dividend_type"`
	DeclarationDate string  `json:"declaration_date" parquet:"declaration_date"`
	ExDividendDate  string  `json:"ex_dividend_date" parquet:"ex_dividend_date"`
	Frequency       int     `json:"frequency" parquet:"frequency"`
	PayDate         string  `json:"pay_date" parquet:"pay_date"`
	RecordDate      string  `json:"record_date" parquet:"record_date"`
}

func fetchSplitsRange(ctx context.Context, client *resty.Client, limiter *rate.Limiter, start, end time.Time) (corporateActions, []massiveSplit, error) {
	out := newCorporateActions(256)
	logger := zerolog.Ctx(ctx)

	windows := splitFetchWindows(start, end, numFetchWindows)

	var (
		mu  sync.Mutex
		all []massiveSplit
	)

	g, gctx := errgroup.WithContext(ctx)

	for i, w := range windows {
		g.Go(func() error {
			return paginateOneSplitWindow(gctx, client, limiter, logger, i, w, &mu, &all, out)
		})
	}

	if err := g.Wait(); err != nil {
		return nil, nil, err
	}

	return out, all, nil
}

func paginateOneSplitWindow(ctx context.Context, client *resty.Client, limiter *rate.Limiter, logger *zerolog.Logger, idx int, w fetchWindow, mu *sync.Mutex, all *[]massiveSplit, out corporateActions) error {
	pageCount := 0
	windowRecords := 0
	started := time.Now()
	hb := rate.Sometimes{Interval: 15 * time.Second}
	label := fmt.Sprintf("splits[%d]", idx)

	return paginateMassive(ctx, client, limiter, splitsURL, map[string]string{
		"execution_date.gte": w.start.Format("2006-01-02"),
		"execution_date.lte": w.end.Format("2006-01-02"),
		"limit":              "1000",
	}, func(raw json.RawMessage) error {
		var rows []massiveSplit
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}

		pageCount++
		windowRecords += len(rows)

		mu.Lock()

		*all = append(*all, rows...)

		for _, r := range rows {
			if r.SplitFrom == 0 {
				continue
			}

			factor := r.SplitTo / r.SplitFrom
			out.set(massiveTicker2PvTicker(r.Ticker), r.ExecutionDate, factor)
		}
		mu.Unlock()

		currentDate := ""
		if len(rows) > 0 {
			currentDate = rows[len(rows)-1].ExecutionDate
		}

		logRESTProgress(logger, &hb, label, started, w.start, w.end, pageCount, windowRecords, currentDate)

		return nil
	})
}

func fetchDividendsRange(ctx context.Context, client *resty.Client, limiter *rate.Limiter, start, end time.Time) (corporateActions, []massiveDividend, error) {
	out := newCorporateActions(1024)
	logger := zerolog.Ctx(ctx)

	windows := splitFetchWindows(start, end, numFetchWindows)

	var (
		mu  sync.Mutex
		all []massiveDividend
	)

	g, gctx := errgroup.WithContext(ctx)

	for i, w := range windows {
		g.Go(func() error {
			return paginateOneDividendWindow(gctx, client, limiter, logger, i, w, &mu, &all, out)
		})
	}

	if err := g.Wait(); err != nil {
		return nil, nil, err
	}

	return out, all, nil
}

func paginateOneDividendWindow(ctx context.Context, client *resty.Client, limiter *rate.Limiter, logger *zerolog.Logger, idx int, w fetchWindow, mu *sync.Mutex, all *[]massiveDividend, out corporateActions) error {
	pageCount := 0
	windowRecords := 0
	started := time.Now()
	hb := rate.Sometimes{Interval: 15 * time.Second}
	label := fmt.Sprintf("dividends[%d]", idx)

	return paginateMassive(ctx, client, limiter, dividendsURL, map[string]string{
		"ex_dividend_date.gte": w.start.Format("2006-01-02"),
		"ex_dividend_date.lte": w.end.Format("2006-01-02"),
		"limit":                "1000",
	}, func(raw json.RawMessage) error {
		var rows []massiveDividend
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}

		pageCount++
		windowRecords += len(rows)

		mu.Lock()

		*all = append(*all, rows...)

		for _, r := range rows {
			if r.CashAmount == 0 {
				continue
			}

			out.set(massiveTicker2PvTicker(r.Ticker), r.ExDividendDate, r.CashAmount)
		}
		mu.Unlock()

		currentDate := ""
		if len(rows) > 0 {
			currentDate = rows[len(rows)-1].ExDividendDate
		}

		logRESTProgress(logger, &hb, label, started, w.start, w.end, pageCount, windowRecords, currentDate)

		return nil
	})
}

// paginateMassive walks every page of a Massive REST listing endpoint,
// invoking handle once per page with the raw results array. The
// initial call uses params; subsequent calls follow next_url which
// already encodes the original query parameters. Progress logging is
// the responsibility of the caller (which has access to parsed rows
// and can surface dataset-aware metrics like processing_date and ETA).
func paginateMassive(ctx context.Context, client *resty.Client, limiter *rate.Limiter, baseURL string, params map[string]string, handle func(json.RawMessage) error) error {
	logger := zerolog.Ctx(ctx)

	url := baseURL
	queryParams := params

	pageCount := 0
	started := time.Now()

	for {
		if err := limiter.Wait(ctx); err != nil {
			return err
		}

		var resp massiveResponse

		req := client.R().SetContext(ctx).SetResult(&resp)
		for k, v := range queryParams {
			req = req.SetQueryParam(k, v)
		}

		httpResp, err := req.Get(url)
		if err != nil {
			return err
		}

		if httpResp.StatusCode() >= 300 {
			return fmt.Errorf("%w (%d): %s", ErrInvalidStatusCode, httpResp.StatusCode(), string(httpResp.Body()))
		}

		pageCount++

		if resp.Results != nil {
			if err := handle(json.RawMessage(*resp.Results)); err != nil {
				return err
			}
		}

		if resp.Next == "" {
			logger.Info().
				Str("endpoint", baseURL).
				Int("pages", pageCount).
				Str("elapsed", time.Since(started).Round(time.Second).String()).
				Msg("paginated REST complete")

			return nil
		}

		url = resp.Next
		queryParams = nil
	}
}

// logRESTProgress emits a heartbeat-rate-limited progress log for a
// paginated REST fetch. currentDate is the oldest record's date on the
// most recent page; Polygon returns descending date order, so this is
// the leading edge moving from end toward start as pagination
// advances. The function computes a progress percentage and ETA from
// that position.
func logRESTProgress(logger *zerolog.Logger, hb *rate.Sometimes, label string, started, start, end time.Time, pages, records int, currentDate string) {
	hb.Do(func() {
		var pct float64

		var eta time.Duration

		if t, err := time.Parse("2006-01-02", currentDate); err == nil {
			total := end.Sub(start)
			progressed := end.Sub(t)

			if total > 0 {
				pct = float64(progressed) / float64(total) * 100
				if pct < 0 {
					pct = 0
				}

				if pct > 100 {
					pct = 100
				}
			}
		}

		elapsed := time.Since(started)
		if pct > 0 && pct < 100 {
			eta = time.Duration(float64(elapsed) * (100 - pct) / pct)
		}

		logger.Info().
			Str("endpoint", label).
			Int("pages", pages).
			Int("records", records).
			Str("processing_date", currentDate).
			Str("progress", fmt.Sprintf("%.1f%%", pct)).
			Str("elapsed", elapsed.Round(time.Second).String()).
			Str("eta", eta.Round(time.Second).String()).
			Msg("paginated REST in progress")
	})
}
