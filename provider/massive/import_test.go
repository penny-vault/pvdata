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
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("Massive file import", func() {
	Describe("parseBackupDate", func() {
		It("parses a YYYY-MM-DD.parquet basename anywhere on the path", func() {
			d, err := parseBackupDate("/var/backup/sub/2024/2024-03-14.parquet")
			Expect(err).NotTo(HaveOccurred())
			Expect(d).To(Equal(time.Date(2024, 3, 14, 0, 0, 0, 0, time.UTC)))
		})

		It("rejects filenames that don't match the layout", func() {
			_, err := parseBackupDate("/tmp/some_other.parquet")
			Expect(err).To(HaveOccurred())
		})

		It("rejects an impossible date even when the filename matches", func() {
			_, err := parseBackupDate("/tmp/2024-13-40.parquet")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("corporateActionsBaseDir", func() {
		It("returns the grandparent directory of the daily file", func() {
			base, err := corporateActionsBaseDir("/var/backup/sub_a/2024/2024-03-14.parquet")
			Expect(err).NotTo(HaveOccurred())

			absBase, err := filepath.Abs("/var/backup/sub_a")
			Expect(err).NotTo(HaveOccurred())
			Expect(base).To(Equal(absBase))
		})
	})

	Describe("loadSplitsForYear", func() {
		It("projects rows into corporateActions keyed by pv ticker and execution date", func() {
			tmp := tempDir()
			splitsDir := filepath.Join(tmp, "splits")
			Expect(os.MkdirAll(splitsDir, 0o755)).To(Succeed())

			rows := []massiveSplit{
				{ID: "a", Ticker: "AAA", ExecutionDate: "2024-03-01", SplitFrom: 1, SplitTo: 2},
				{ID: "b", Ticker: "BRK.A", ExecutionDate: "2024-08-15", SplitFrom: 1, SplitTo: 3},
				{ID: "c", Ticker: "ZZZ", ExecutionDate: "2024-09-01", SplitFrom: 0, SplitTo: 0},
			}
			Expect(writeFlatFileBackup(filepath.Join(splitsDir, "2024.parquet"), rows)).To(Succeed())

			splits, err := loadSplitsForYear(tmp, 2024)
			Expect(err).NotTo(HaveOccurred())

			Expect(splits.lookup("AAA", "2024-03-01")).To(Equal(2.0))
			// massiveTicker2PvTicker maps "." to "/"
			Expect(splits.lookup("BRK/A", "2024-08-15")).To(Equal(3.0))
			// Rows with SplitFrom == 0 are skipped
			Expect(splits.lookup("ZZZ", "2024-09-01")).To(Equal(0.0))
		})

		It("returns an error when the splits parquet is missing", func() {
			tmp := tempDir()
			_, err := loadSplitsForYear(tmp, 2024)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("loadDividendsForYear", func() {
		It("projects rows into corporateActions keyed by pv ticker and ex-dividend date", func() {
			tmp := tempDir()
			divsDir := filepath.Join(tmp, "dividends")
			Expect(os.MkdirAll(divsDir, 0o755)).To(Succeed())

			rows := []massiveDividend{
				{ID: "a", Ticker: "AAA", ExDividendDate: "2024-03-01", CashAmount: 0.50},
				{ID: "b", Ticker: "BBB", ExDividendDate: "2024-06-15", CashAmount: 0},
			}
			Expect(writeFlatFileBackup(filepath.Join(divsDir, "2024.parquet"), rows)).To(Succeed())

			divs, err := loadDividendsForYear(tmp, 2024)
			Expect(err).NotTo(HaveOccurred())

			Expect(divs.lookup("AAA", "2024-03-01")).To(Equal(0.50))
			Expect(divs.lookup("BBB", "2024-06-15")).To(Equal(0.0))
		})
	})

	Describe("corporateActionsCache", func() {
		It("reads each (baseDir, year) pair only once", func() {
			tmp := tempDir()
			writeYearActions(tmp, 2024,
				[]massiveSplit{{ID: "s", Ticker: "AAA", ExecutionDate: "2024-03-01", SplitFrom: 1, SplitTo: 2}},
				[]massiveDividend{{ID: "d", Ticker: "AAA", ExDividendDate: "2024-06-15", CashAmount: 0.25}},
			)

			cache := newCorporateActionsCache()

			splits1, divs1, err := cache.get(tmp, 2024)
			Expect(err).NotTo(HaveOccurred())

			// Delete the underlying files to prove the second call is served from cache.
			Expect(os.RemoveAll(filepath.Join(tmp, "splits"))).To(Succeed())
			Expect(os.RemoveAll(filepath.Join(tmp, "dividends"))).To(Succeed())

			splits2, divs2, err := cache.get(tmp, 2024)
			Expect(err).NotTo(HaveOccurred())

			Expect(splits2.lookup("AAA", "2024-03-01")).To(Equal(splits1.lookup("AAA", "2024-03-01")))
			Expect(divs2.lookup("AAA", "2024-06-15")).To(Equal(divs1.lookup("AAA", "2024-06-15")))
		})
	})

	Describe("emitEODRows", func() {
		var (
			sub      *library.Subscription
			universe *data.AssetHistory
			d        time.Time
		)

		BeforeEach(func() {
			sub = &library.Subscription{Name: "Massive EOD", ID: uuid.MustParse("3a85a000-0000-0000-0000-000000000000")}
			d = time.Date(2024, 3, 14, 0, 0, 0, 0, time.UTC)

			universe = data.NewAssetHistory([]*data.Asset{
				{Ticker: "AAA", CompositeFigi: "BBG000AAAAA1"},
				{Ticker: "BRK/A", CompositeFigi: "BBG000BBBBB2"},
			})
		})

		It("applies splits and dividends, looks up FIGI, and emits an EodQuote per row", func() {
			splits := newCorporateActions(2)
			splits.set("AAA", "2024-03-14", 2.0)

			divs := newCorporateActions(2)
			divs.set("BRK/A", "2024-03-14", 1.25)

			rows := []aggRow{
				{Ticker: "AAA", Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, WindowStart: 1, Transactions: 5},
				{Ticker: "BRK.A", Open: 500000, High: 510000, Low: 495000, Close: 505000, Volume: 5, WindowStart: 1},
			}

			out := make(chan *data.Observation, 4)
			n, err := emitEODRows(context.Background(), sub, universe, splits, divs, d, rows, out)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(2))
			close(out)

			collected := drainObservations(out)
			Expect(collected).To(HaveLen(2))

			byTicker := map[string]*data.Eod{}
			for _, obs := range collected {
				Expect(obs.EodQuote).NotTo(BeNil())
				byTicker[obs.EodQuote.Ticker] = obs.EodQuote
			}

			Expect(byTicker["AAA"].Split).To(Equal(2.0))
			Expect(byTicker["AAA"].Dividend).To(Equal(0.0))
			Expect(byTicker["AAA"].CompositeFigi).To(Equal("BBG000AAAAA1"))
			Expect(byTicker["BRK/A"].Split).To(Equal(1.0))
			Expect(byTicker["BRK/A"].Dividend).To(Equal(1.25))
			Expect(byTicker["BRK/A"].CompositeFigi).To(Equal("BBG000BBBBB2"))
		})

		It("drops rows whose ticker is not in the universe on that date", func() {
			rows := []aggRow{
				{Ticker: "AAA", Open: 10, Close: 11, Volume: 100},
				{Ticker: "ZZZ_DELISTED", Open: 1, Close: 1, Volume: 1},
			}

			out := make(chan *data.Observation, 4)
			n, err := emitEODRows(context.Background(), sub, universe, newCorporateActions(0), newCorporateActions(0), d, rows, out)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(1))
		})

		It("anchors the event timestamp to 4pm New York on the trading day", func() {
			rows := []aggRow{{Ticker: "AAA", Open: 10, Close: 11, Volume: 100}}

			out := make(chan *data.Observation, 1)
			_, err := emitEODRows(context.Background(), sub, universe, newCorporateActions(0), newCorporateActions(0), d, rows, out)
			Expect(err).NotTo(HaveOccurred())
			close(out)

			obs := <-out
			nyc, _ := time.LoadLocation("America/New_York")
			Expect(obs.EodQuote.Date).To(Equal(time.Date(2024, 3, 14, 16, 0, 0, 0, nyc)))
		})
	})

	Describe("emitMinuteRows", func() {
		It("emits IntradayBar with Date derived from WindowStart nanoseconds", func() {
			sub := &library.Subscription{Name: "Massive 1m", ID: uuid.MustParse("3a85a000-0000-0000-0000-000000000000")}
			universe := data.NewAssetHistory([]*data.Asset{
				{Ticker: "AAA", CompositeFigi: "BBG000AAAAA1"},
			})

			eventTime := time.Date(2024, 3, 14, 13, 30, 0, 0, time.UTC)
			rows := []aggRow{
				{Ticker: "AAA", Open: 10, High: 10.5, Low: 9.9, Close: 10.2, Volume: 100, WindowStart: eventTime.UnixNano()},
			}

			out := make(chan *data.Observation, 1)
			n, err := emitMinuteRows(context.Background(), sub, universe, time.Date(2024, 3, 14, 0, 0, 0, 0, time.UTC), rows, out)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(1))
			close(out)

			obs := <-out
			Expect(obs.IntradayBar).NotTo(BeNil())
			Expect(obs.IntradayBar.Ticker).To(Equal("AAA"))
			Expect(obs.IntradayBar.CompositeFigi).To(Equal("BBG000AAAAA1"))
			Expect(obs.IntradayBar.Date.UTC()).To(Equal(eventTime))
		})
	})

	Describe("importMinuteFiles", func() {
		It("reads a parquet backup and emits one Observation per universe-matched row", func() {
			tmp := tempDir()
			yearDir := filepath.Join(tmp, "2024")
			Expect(os.MkdirAll(yearDir, 0o755)).To(Succeed())

			rows := []aggRow{
				{Ticker: "AAA", Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100, WindowStart: time.Date(2024, 3, 14, 14, 30, 0, 0, time.UTC).UnixNano()},
				{Ticker: "UNKNOWN", Open: 1, Close: 1, Volume: 1, WindowStart: time.Date(2024, 3, 14, 14, 31, 0, 0, time.UTC).UnixNano()},
			}
			path := filepath.Join(yearDir, "2024-03-14.parquet")
			Expect(writeFlatFileBackup(path, rows)).To(Succeed())

			sub := &library.Subscription{Name: "Massive 1m", ID: uuid.MustParse("3a85a000-0000-0000-0000-000000000000")}
			universe := data.NewAssetHistory([]*data.Asset{
				{Ticker: "AAA", CompositeFigi: "BBG000AAAAA1"},
			})

			out := make(chan *data.Observation, 4)
			n, err := importMinuteFiles(context.Background(), sub, universe, []string{path}, out)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(1))
		})
	})

	Describe("expandImportPaths", func() {
		It("walks a directory tree and collects every daily backup, ignoring splits and dividends", func() {
			tmp := tempDir()
			Expect(os.MkdirAll(filepath.Join(tmp, "2023"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(tmp, "2024"), 0o755)).To(Succeed())

			daily := []string{
				filepath.Join(tmp, "2023", "2023-12-29.parquet"),
				filepath.Join(tmp, "2024", "2024-01-02.parquet"),
				filepath.Join(tmp, "2024", "2024-03-14.parquet"),
			}
			for _, path := range daily {
				Expect(writeFlatFileBackup(path, []aggRow{{Ticker: "AAA", Close: 10}})).To(Succeed())
			}

			// Corporate-actions tree at the same root must be skipped.
			writeYearActions(tmp, 2024,
				[]massiveSplit{{ID: "s", Ticker: "AAA", ExecutionDate: "2024-03-14", SplitFrom: 1, SplitTo: 2}},
				[]massiveDividend{{ID: "d", Ticker: "AAA", ExDividendDate: "2024-03-14", CashAmount: 0.10}},
			)

			// A leftover *.parquet.tmp must not be returned.
			Expect(os.WriteFile(filepath.Join(tmp, "2024", "2024-04-01.parquet.tmp"), []byte("partial"), 0o644)).To(Succeed())

			got, err := expandImportPaths([]string{tmp})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(daily))
		})

		It("accepts a mix of files and directories", func() {
			tmp := tempDir()
			Expect(os.MkdirAll(filepath.Join(tmp, "2024"), 0o755)).To(Succeed())

			dailyA := filepath.Join(tmp, "2024", "2024-03-14.parquet")
			Expect(writeFlatFileBackup(dailyA, []aggRow{{Ticker: "AAA", Close: 10}})).To(Succeed())

			loose := filepath.Join(tempDir(), "2024-06-01.parquet")
			Expect(writeFlatFileBackup(loose, []aggRow{{Ticker: "BBB", Close: 11}})).To(Succeed())

			got, err := expandImportPaths([]string{tmp, loose})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(ConsistOf(dailyA, loose))
		})

		It("returns an empty result for a directory with no matching files", func() {
			tmp := tempDir()
			got, err := expandImportPaths([]string{tmp})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeEmpty())
		})

		It("returns an error when a path does not exist", func() {
			_, err := expandImportPaths([]string{"/nonexistent/path/deliberately/missing"})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("importEODFiles", func() {
		It("joins each row against the colocated splits and dividends parquet for its year", func() {
			tmp := tempDir()
			yearDir := filepath.Join(tmp, "2024")
			Expect(os.MkdirAll(yearDir, 0o755)).To(Succeed())

			writeYearActions(tmp, 2024,
				[]massiveSplit{{ID: "s", Ticker: "AAA", ExecutionDate: "2024-03-14", SplitFrom: 1, SplitTo: 2}},
				[]massiveDividend{{ID: "d", Ticker: "AAA", ExDividendDate: "2024-03-14", CashAmount: 0.25}},
			)

			rows := []aggRow{
				{Ticker: "AAA", Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100},
			}
			path := filepath.Join(yearDir, "2024-03-14.parquet")
			Expect(writeFlatFileBackup(path, rows)).To(Succeed())

			sub := &library.Subscription{Name: "Massive EOD", ID: uuid.MustParse("3a85a000-0000-0000-0000-000000000000")}
			universe := data.NewAssetHistory([]*data.Asset{
				{Ticker: "AAA", CompositeFigi: "BBG000AAAAA1"},
			})

			out := make(chan *data.Observation, 4)
			n, err := importEODFiles(context.Background(), sub, universe, []string{path}, out)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(1))
			close(out)

			obs := <-out
			Expect(obs.EodQuote).NotTo(BeNil())
			Expect(obs.EodQuote.Split).To(Equal(2.0))
			Expect(obs.EodQuote.Dividend).To(Equal(0.25))
		})

		It("returns an error when the splits parquet for the year is missing", func() {
			tmp := tempDir()
			yearDir := filepath.Join(tmp, "2024")
			Expect(os.MkdirAll(yearDir, 0o755)).To(Succeed())

			path := filepath.Join(yearDir, "2024-03-14.parquet")
			Expect(writeFlatFileBackup(path, []aggRow{{Ticker: "AAA", Close: 10, Volume: 100}})).To(Succeed())

			sub := &library.Subscription{Name: "Massive EOD", ID: uuid.MustParse("3a85a000-0000-0000-0000-000000000000")}
			universe := data.NewAssetHistory([]*data.Asset{
				{Ticker: "AAA", CompositeFigi: "BBG000AAAAA1"},
			})

			out := make(chan *data.Observation, 4)
			_, err := importEODFiles(context.Background(), sub, universe, []string{path}, out)
			Expect(err).To(HaveOccurred())
		})
	})
})

func tempDir() string {
	tmp, err := os.MkdirTemp("", "massive-import-*")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(os.RemoveAll, tmp)

	return tmp
}

func writeYearActions(baseDir string, year int, splits []massiveSplit, divs []massiveDividend) {
	splitsDir := filepath.Join(baseDir, "splits")
	Expect(os.MkdirAll(splitsDir, 0o755)).To(Succeed())
	Expect(writeFlatFileBackup(filepath.Join(splitsDir, fmtYear(year)), splits)).To(Succeed())

	divsDir := filepath.Join(baseDir, "dividends")
	Expect(os.MkdirAll(divsDir, 0o755)).To(Succeed())
	Expect(writeFlatFileBackup(filepath.Join(divsDir, fmtYear(year)), divs)).To(Succeed())
}

func fmtYear(year int) string {
	return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") + ".parquet"
}

func drainObservations(ch <-chan *data.Observation) []*data.Observation {
	var out []*data.Observation
	for obs := range ch {
		out = append(out, obs)
	}

	return out
}
