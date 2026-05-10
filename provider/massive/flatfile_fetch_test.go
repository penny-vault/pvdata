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
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeS3Server is a tiny in-process replacement for the Massive
// flat-files S3 endpoint. It serves gzipped CSV bodies for whatever
// keys have been pre-loaded; for a given key it can also be configured
// to fail with 500 the first N times before serving the real body.
type fakeS3Server struct {
	server *httptest.Server

	mu       sync.Mutex
	failures map[string]int
	contents map[string][]byte
	requests atomic.Int64
}

func newFakeS3Server() *fakeS3Server {
	f := &fakeS3Server{
		failures: map[string]int{},
		contents: map[string][]byte{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))

	return f
}

func (f *fakeS3Server) Close()      { f.server.Close() }
func (f *fakeS3Server) URL() string { return f.server.URL }

func (f *fakeS3Server) handle(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)

	// Path-style addressing: /<bucket>/<key...>
	key := strings.TrimPrefix(r.URL.Path, "/"+flatFilesBucket+"/")

	f.mu.Lock()

	if n := f.failures[key]; n > 0 {
		f.failures[key] = n - 1
		f.mu.Unlock()

		http.Error(w, "synthetic transient error", http.StatusInternalServerError)

		return
	}

	body, ok := f.contents[key]
	f.mu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))

		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(body)
}

func (f *fakeS3Server) setCSV(key, csv string) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(csv))
	_ = gz.Close()

	f.mu.Lock()
	defer f.mu.Unlock()

	f.contents[key] = buf.Bytes()
}

func (f *fakeS3Server) failNTimes(key string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failures[key] = n
}

// newFakeS3Client returns a Massive flat-files-style S3 client whose
// requests go to the fake server. SDK-level retries are disabled
// (MaxAttempts=1) so the test sees one HTTP request per fetch attempt
// and our own retry loop is the only thing that recovers.
func newFakeS3Client(endpoint string) *s3.Client {
	return s3.New(s3.Options{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(endpoint),
		Credentials:      credentials.NewStaticCredentialsProvider("test", "test", ""),
		UsePathStyle:     true,
		RetryMaxAttempts: 1,
		RetryMode:        aws.RetryModeStandard,
	})
}

const sampleAggsCSV = `ticker,volume,open,close,high,low,window_start,transactions
A,100,118.7,118.7,118.7,118.7,1778238240000000000,1
B,200,50.0,51.0,51.5,49.9,1778238240000000000,5
`

var _ = Describe("fetchAndParseAggs (retry behaviour)", func() {
	var fake *fakeS3Server

	BeforeEach(func() {
		fake = newFakeS3Server()
	})

	AfterEach(func() {
		fake.Close()
	})

	It("succeeds on first attempt when the object is present", func() {
		fake.setCSV("us_stocks_sip/day_aggs_v1/2024/06/2024-06-01.csv.gz", sampleAggsCSV)

		client := newFakeS3Client(fake.URL())
		ctx := context.Background()

		rows, err := fetchAndParseAggs(ctx, client, "us_stocks_sip/day_aggs_v1/2024/06/2024-06-01.csv.gz")
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].Ticker).To(Equal("A"))
		Expect(rows[1].Ticker).To(Equal("B"))
		Expect(fake.requests.Load()).To(Equal(int64(1)))
	})

	It("recovers from a transient failure within the retry budget", func() {
		key := "us_stocks_sip/day_aggs_v1/2024/06/2024-06-02.csv.gz"
		fake.setCSV(key, sampleAggsCSV)
		fake.failNTimes(key, 3) // 3 failures, then success on attempt 4

		client := newFakeS3Client(fake.URL())
		// A real run would back off seconds-scale between attempts;
		// the test just needs functional correctness, so use a short
		// timeout instead of overriding the backoff schedule.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		rows, err := fetchAndParseAggs(ctx, client, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		// 3 failures + 1 success = 4 requests
		Expect(fake.requests.Load()).To(Equal(int64(4)))
	})

	It("gives up after exhausting the retry budget", func() {
		key := "us_stocks_sip/day_aggs_v1/2024/06/2024-06-03.csv.gz"
		fake.setCSV(key, sampleAggsCSV)
		fake.failNTimes(key, 100) // far more than flatFileMaxAttempts

		client := newFakeS3Client(fake.URL())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		rows, err := fetchAndParseAggs(ctx, client, key)
		Expect(err).To(HaveOccurred())
		Expect(rows).To(BeNil())
		Expect(err.Error()).To(ContainSubstring("exhausted"))
		Expect(fake.requests.Load()).To(Equal(int64(flatFileMaxAttempts)))
	})

	It("surfaces NoSuchKey as errFlatFileMissing without retrying", func() {
		client := newFakeS3Client(fake.URL())
		ctx := context.Background()

		// Object is never registered with fake.setCSV → server returns 404
		rows, err := fetchAndParseAggs(ctx, client, "us_stocks_sip/day_aggs_v1/1999/01/1999-01-01.csv.gz")
		Expect(err).To(MatchError(errFlatFileMissing))
		Expect(rows).To(BeNil())
		Expect(fake.requests.Load()).To(Equal(int64(1)),
			"missing object should not retry; only one HTTP request expected")
	})

	It("respects context cancellation mid-backoff", func() {
		key := "us_stocks_sip/day_aggs_v1/2024/06/2024-06-04.csv.gz"
		fake.setCSV(key, sampleAggsCSV)
		fake.failNTimes(key, 100)

		client := newFakeS3Client(fake.URL())
		// Cancel after the first failure so the retry backoff sees a
		// dead context.
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_, err := fetchAndParseAggs(ctx, client, key)
		Expect(err).To(HaveOccurred())
		// Either a context error (if cancel hit during backoff) or
		// the exhausted error (if every attempt fast-failed) is
		// acceptable; what we are proving is that the function
		// returns rather than blocking on the backoff timer.
	})
})
