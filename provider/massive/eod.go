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
	"compress/gzip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
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
	"golang.org/x/time/rate"
)

const (
	flatFilesEndpoint = "https://files.massive.com"
	flatFilesBucket   = "flatfiles"
	dayAggsKeyFormat  = "us_stocks_sip/day_aggs_v1/%04d/%02d/%s.csv.gz"

	splitsURL    = "https://api.massive.com/v3/reference/splits"
	dividendsURL = "https://api.massive.com/v3/reference/dividends"

	defaultEODLookback = 7 * 24 * time.Hour
	defaultRateLimit   = 5000
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
	restClient := resty.New().SetQueryParam("apiKey", sub.Config["apiKey"])
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

	dbAssets, err := data.ActiveAssets(ctx, conn, sub.DataTablesMap[data.AssetKey])

	conn.Release()

	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets")

		runSummary.Status = data.RunFailed

		return
	}

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)

	tickerToFigi := make(map[string]string, len(dbAssets))
	for _, a := range dbAssets {
		if a.CompositeFigi == "" {
			continue
		}

		if tickerFilter != "" && !strings.EqualFold(a.Ticker, tickerFilter) {
			continue
		}

		if figiFilter != "" && a.CompositeFigi != figiFilter {
			continue
		}

		tickerToFigi[a.Ticker] = a.CompositeFigi
	}

	if len(tickerToFigi) == 0 {
		logger.Warn().Msg("no assets in scope for massive EOD; skipping run")
		return
	}

	logger.Info().
		Time("start", start).
		Time("end", end).
		Int("scope_assets", len(tickerToFigi)).
		Msg("massive flat-files EOD loader starting")

	splits, err := fetchSplitsRange(ctx, restClient, limiter, start, end)
	if err != nil {
		logger.Error().Err(err).Msg("could not fetch splits from massive REST")

		runSummary.Status = data.RunFailed

		return
	}

	divs, err := fetchDividendsRange(ctx, restClient, limiter, start, end)
	if err != nil {
		logger.Error().Err(err).Msg("could not fetch dividends from massive REST")

		runSummary.Status = data.RunFailed

		return
	}

	logger.Info().
		Int("split_tickers", splits.tickerCount()).
		Int("dividend_tickers", divs.tickerCount()).
		Msg("loaded corporate actions for window")

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if isWeekend(d) {
			continue
		}

		n, err := streamDayAggsForDate(ctx, s3Client, sub, tickerToFigi, splits, divs, d, out)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				runSummary.Status = data.RunFailed
				return
			}

			logger.Warn().Err(err).Time("date", d).Msg("day aggregates fetch failed; skipping date")

			continue
		}

		numObs += n
	}
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
// for date d. Rows whose ticker is not in tickerToFigi are dropped.
// A NoSuchKey response (non-trading day, or file not yet published)
// is treated as an empty result rather than an error.
func streamDayAggsForDate(ctx context.Context, client *s3.Client, sub *library.Subscription, tickerToFigi map[string]string, splits, divs corporateActions, d time.Time, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)
	key := dayAggsKey(d)

	obj, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(flatFilesBucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNoSuchKey(err) {
			logger.Debug().Str("key", key).Msg("flat file not present; skipping date")
			return 0, nil
		}

		return 0, fmt.Errorf("getobject %s: %w", key, err)
	}

	defer obj.Body.Close()

	gz, err := gzip.NewReader(obj.Body)
	if err != nil {
		return 0, fmt.Errorf("gunzip %s: %w", key, err)
	}

	defer gz.Close()

	eventTime, err := buildMassiveEodEvent(d)
	if err != nil {
		return 0, err
	}

	dateStr := d.Format("2006-01-02")

	rows, err := parseDayAggs(gz)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}

	n := 0

	for _, row := range rows {
		ticker := massiveTicker2PvTicker(row.Ticker)

		figi, ok := tickerToFigi[ticker]
		if !ok {
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

// dayAggRow is a single parsed row from a day_aggs_v1 flat file.
type dayAggRow struct {
	Ticker string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// parseDayAggs reads CSV rows from r (already gunzipped) and returns
// the parsed rows. The expected header is
// "ticker,volume,open,close,high,low,window_start,transactions" but
// columns are looked up by name so order/extra columns are tolerated.
func parseDayAggs(r io.Reader) ([]dayAggRow, error) {
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

	var rows []dayAggRow

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return rows, fmt.Errorf("read row: %w", err)
		}

		rows = append(rows, dayAggRow{
			Ticker: record[cols["ticker"]],
			Open:   parseFloat(record[cols["open"]]),
			High:   parseFloat(record[cols["high"]]),
			Low:    parseFloat(record[cols["low"]]),
			Close:  parseFloat(record[cols["close"]]),
			Volume: parseFloat(record[cols["volume"]]),
		})
	}

	return rows, nil
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

type massiveSplit struct {
	Ticker        string  `json:"ticker"`
	ExecutionDate string  `json:"execution_date"`
	SplitFrom     float64 `json:"split_from"`
	SplitTo       float64 `json:"split_to"`
}

type massiveDividend struct {
	Ticker         string  `json:"ticker"`
	ExDividendDate string  `json:"ex_dividend_date"`
	CashAmount     float64 `json:"cash_amount"`
}

func fetchSplitsRange(ctx context.Context, client *resty.Client, limiter *rate.Limiter, start, end time.Time) (corporateActions, error) {
	out := newCorporateActions(256)

	err := paginateMassive(ctx, client, limiter, splitsURL, map[string]string{
		"execution_date.gte": start.Format("2006-01-02"),
		"execution_date.lte": end.Format("2006-01-02"),
		"limit":              "1000",
	}, func(raw json.RawMessage) error {
		var rows []massiveSplit
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}

		for _, r := range rows {
			if r.SplitFrom == 0 {
				continue
			}

			factor := r.SplitTo / r.SplitFrom
			out.set(massiveTicker2PvTicker(r.Ticker), r.ExecutionDate, factor)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func fetchDividendsRange(ctx context.Context, client *resty.Client, limiter *rate.Limiter, start, end time.Time) (corporateActions, error) {
	out := newCorporateActions(1024)

	err := paginateMassive(ctx, client, limiter, dividendsURL, map[string]string{
		"ex_dividend_date.gte": start.Format("2006-01-02"),
		"ex_dividend_date.lte": end.Format("2006-01-02"),
		"limit":                "1000",
	}, func(raw json.RawMessage) error {
		var rows []massiveDividend
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}

		for _, r := range rows {
			if r.CashAmount == 0 {
				continue
			}

			out.set(massiveTicker2PvTicker(r.Ticker), r.ExDividendDate, r.CashAmount)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// paginateMassive walks every page of a Massive REST listing endpoint,
// invoking handle once per page with the raw results array. The
// initial call uses params; subsequent calls follow next_url which
// already encodes the original query parameters.
func paginateMassive(ctx context.Context, client *resty.Client, limiter *rate.Limiter, baseURL string, params map[string]string, handle func(json.RawMessage) error) error {
	url := baseURL
	queryParams := params

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

		if resp.Results != nil {
			if err := handle(json.RawMessage(*resp.Results)); err != nil {
				return err
			}
		}

		if resp.Next == "" {
			return nil
		}

		url = resp.Next
		queryParams = nil
	}
}
