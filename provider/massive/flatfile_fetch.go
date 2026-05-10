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
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rs/zerolog"
)

// errFlatFileMissing is returned by fetchAndParseAggs when S3 reports
// NoSuchKey for the requested object. Callers should treat this as a
// non-trading day or unpublished file rather than an error.
var errFlatFileMissing = errors.New("flat file missing")

const (
	// flatFileMaxAttempts caps the number of retry attempts for a
	// single day's flat-file fetch. The AWS SDK already retries
	// transport-level errors before delivering the response body;
	// this loop covers mid-stream failures (HTTP/2 GOAWAY during
	// body read, gzip truncation, parse errors) by re-issuing the
	// whole GetObject + read + parse sequence.
	flatFileMaxAttempts = 5

	// flatFileMaxBackoff caps the exponential backoff between
	// retries so a single bad day cannot stall the daily loop for
	// minutes.
	flatFileMaxBackoff = 30 * time.Second
)

// fetchAndParseAggs downloads, gunzips, and parses one day's aggregates
// flat file. Transient failures (transport errors after body delivery,
// gzip read errors, CSV parse errors) are retried with exponential
// backoff up to flatFileMaxAttempts times. NoSuchKey is surfaced as
// errFlatFileMissing without retry; context cancellation aborts
// immediately.
func fetchAndParseAggs(ctx context.Context, client *s3.Client, key string) ([]aggRow, error) {
	logger := zerolog.Ctx(ctx)

	var lastErr error

	for attempt := range flatFileMaxAttempts {
		if attempt > 0 {
			wait := time.Duration(1<<attempt) * time.Second
			if wait > flatFileMaxBackoff {
				wait = flatFileMaxBackoff
			}

			logger.Warn().
				Err(lastErr).
				Str("key", key).
				Int("attempt", attempt+1).
				Dur("wait", wait).
				Msg("retrying flat-file fetch after transient error")

			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		rows, err := tryFetchAndParseAggs(ctx, client, key)
		if err == nil {
			return rows, nil
		}

		if errors.Is(err, errFlatFileMissing) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		lastErr = err
	}

	return nil, fmt.Errorf("flat-file fetch exhausted %d attempts: %w", flatFileMaxAttempts, lastErr)
}

// tryFetchAndParseAggs is a single attempt: GetObject → gunzip →
// parseAggs. The body is fully consumed before returning so a partial
// read or stream interruption surfaces as a normal Go error rather
// than leaking past the function boundary.
func tryFetchAndParseAggs(ctx context.Context, client *s3.Client, key string) ([]aggRow, error) {
	obj, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(flatFilesBucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, errFlatFileMissing
		}

		return nil, fmt.Errorf("getobject %s: %w", key, err)
	}

	defer obj.Body.Close()

	gz, err := gzip.NewReader(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("gunzip %s: %w", key, err)
	}

	defer gz.Close()

	rows, err := parseAggs(gz)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", key, err)
	}

	return rows, nil
}
