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
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// writeArchiveDayFile writes a per-day aggregate parquet file in the
// archive layout (rootDir/<YYYY>/<YYYY-MM-DD>.parquet) for use by the
// archive-walker tests. tickers is the list of tickers that have a
// bar on that day.
func writeArchiveDayFile(rootDir string, date time.Time, tickers []string) {
	rows := make([]aggRow, len(tickers))
	for i, ticker := range tickers {
		rows[i] = aggRow{Ticker: ticker, Volume: 1, Open: 1, Close: 1, High: 1, Low: 1}
	}

	path := filepath.Join(rootDir, date.Format("2006"), date.Format("2006-01-02")+".parquet")
	Expect(writeFlatFileBackup(path, rows)).To(Succeed())
}

var _ = Describe("LoadEODArchive", func() {
	It("returns an empty archive when the root directory is missing", func() {
		archive, err := LoadEODArchive(filepath.Join(GinkgoT().TempDir(), "does-not-exist"))
		Expect(err).NotTo(HaveOccurred())
		Expect(archive).NotTo(BeNil())

		first, last, ok := archive.Lookup("AAPL")
		Expect(ok).To(BeFalse())
		Expect(first.IsZero()).To(BeTrue())
		Expect(last.IsZero()).To(BeTrue())
	})

	It("returns an empty archive when rootDir is empty string", func() {
		archive, err := LoadEODArchive("")
		Expect(err).NotTo(HaveOccurred())
		Expect(archive).NotTo(BeNil())

		start, end := archive.Coverage()
		Expect(start.IsZero()).To(BeTrue())
		Expect(end.IsZero()).To(BeTrue())
	})

	It("records first and last bar dates for each ticker across multiple files", func() {
		root := GinkgoT().TempDir()

		writeArchiveDayFile(root, time.Date(2018, 4, 10, 0, 0, 0, 0, time.UTC), []string{"AAPL", "MSFT"})
		writeArchiveDayFile(root, time.Date(2018, 4, 11, 0, 0, 0, 0, time.UTC), []string{"AAPL"})
		writeArchiveDayFile(root, time.Date(2019, 1, 14, 0, 0, 0, 0, time.UTC), []string{"AAPL", "MSFT", "GOOG"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		aaplFirst, aaplLast, ok := archive.Lookup("AAPL")
		Expect(ok).To(BeTrue())
		Expect(aaplFirst).To(Equal(time.Date(2018, 4, 10, 0, 0, 0, 0, time.UTC)))
		Expect(aaplLast).To(Equal(time.Date(2019, 1, 14, 0, 0, 0, 0, time.UTC)))

		msftFirst, msftLast, ok := archive.Lookup("MSFT")
		Expect(ok).To(BeTrue())
		Expect(msftFirst).To(Equal(time.Date(2018, 4, 10, 0, 0, 0, 0, time.UTC)))
		Expect(msftLast).To(Equal(time.Date(2019, 1, 14, 0, 0, 0, 0, time.UTC)))

		googFirst, googLast, ok := archive.Lookup("GOOG")
		Expect(ok).To(BeTrue())
		Expect(googFirst).To(Equal(time.Date(2019, 1, 14, 0, 0, 0, 0, time.UTC)))
		Expect(googLast).To(Equal(time.Date(2019, 1, 14, 0, 0, 0, 0, time.UTC)))
	})

	It("tracks archive-wide coverage start and end", func() {
		root := GinkgoT().TempDir()

		writeArchiveDayFile(root, time.Date(2018, 4, 10, 0, 0, 0, 0, time.UTC), []string{"AAPL"})
		writeArchiveDayFile(root, time.Date(2019, 1, 14, 0, 0, 0, 0, time.UTC), []string{"AAPL"})
		writeArchiveDayFile(root, time.Date(2015, 7, 1, 0, 0, 0, 0, time.UTC), []string{"AAPL"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		start, end := archive.Coverage()
		Expect(start).To(Equal(time.Date(2015, 7, 1, 0, 0, 0, 0, time.UTC)))
		Expect(end).To(Equal(time.Date(2019, 1, 14, 0, 0, 0, 0, time.UTC)))
	})

	It("normalizes class-share tickers from Massive's dot to pvdata's slash", func() {
		root := GinkgoT().TempDir()

		writeArchiveDayFile(root, time.Date(2020, 3, 15, 0, 0, 0, 0, time.UTC), []string{"BF.A", "BF.B"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		_, _, okSlash := archive.Lookup("BF/A")
		Expect(okSlash).To(BeTrue())

		_, _, okDot := archive.Lookup("BF.A")
		Expect(okDot).To(BeFalse())
	})

	It("skips files whose names do not match the date pattern", func() {
		root := GinkgoT().TempDir()

		writeArchiveDayFile(root, time.Date(2020, 3, 15, 0, 0, 0, 0, time.UTC), []string{"AAPL"})

		// Write an extra file with a non-date name; the walker should
		// ignore it without erroring.
		junk := filepath.Join(root, "2020", "notes.parquet")
		Expect(writeFlatFileBackup(junk, []aggRow{{Ticker: "JUNK", Volume: 1}})).To(Succeed())

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		_, _, okAAPL := archive.Lookup("AAPL")
		Expect(okAAPL).To(BeTrue())

		_, _, okJunk := archive.Lookup("JUNK")
		Expect(okJunk).To(BeFalse())
	})
})

var _ = Describe("EODArchive.Lookup on nil receiver", func() {
	It("returns ok=false without panicking", func() {
		var archive *EODArchive
		_, _, ok := archive.Lookup("AAPL")
		Expect(ok).To(BeFalse())
	})

	It("returns zero coverage from Coverage()", func() {
		var archive *EODArchive
		start, end := archive.Coverage()
		Expect(start.IsZero()).To(BeTrue())
		Expect(end.IsZero()).To(BeTrue())
	})
})

var _ = Describe("EODArchive lifecycle gap splitting", func() {
	It("splits a ticker's history into separate ranges across a gap longer than the lifecycle threshold", func() {
		root := GinkgoT().TempDir()

		// Old issuer 2010-03-01..2010-03-10, then 60 days of silence,
		// then new issuer 2010-05-10..2010-05-15 under the same ticker.
		writeArchiveDayFile(root, time.Date(2010, 3, 1, 0, 0, 0, 0, time.UTC), []string{"AA"})
		writeArchiveDayFile(root, time.Date(2010, 3, 10, 0, 0, 0, 0, time.UTC), []string{"AA"})
		writeArchiveDayFile(root, time.Date(2010, 5, 10, 0, 0, 0, 0, time.UTC), []string{"AA"})
		writeArchiveDayFile(root, time.Date(2010, 5, 15, 0, 0, 0, 0, time.UTC), []string{"AA"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		ranges := archive.Ranges("AA")
		Expect(ranges).To(HaveLen(2))
		Expect(ranges[0].Start).To(Equal(time.Date(2010, 3, 1, 0, 0, 0, 0, time.UTC)))
		Expect(ranges[0].End).To(Equal(time.Date(2010, 3, 10, 0, 0, 0, 0, time.UTC)))
		Expect(ranges[1].Start).To(Equal(time.Date(2010, 5, 10, 0, 0, 0, 0, time.UTC)))
		Expect(ranges[1].End).To(Equal(time.Date(2010, 5, 15, 0, 0, 0, 0, time.UTC)))
	})

	It("keeps a single range when consecutive observations stay within the lifecycle gap", func() {
		root := GinkgoT().TempDir()

		// Weekend + Monday holiday gap is well under 14 days.
		writeArchiveDayFile(root, time.Date(2020, 5, 22, 0, 0, 0, 0, time.UTC), []string{"AAPL"})
		writeArchiveDayFile(root, time.Date(2020, 5, 26, 0, 0, 0, 0, time.UTC), []string{"AAPL"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		ranges := archive.Ranges("AAPL")
		Expect(ranges).To(HaveLen(1))
		Expect(ranges[0].Start).To(Equal(time.Date(2020, 5, 22, 0, 0, 0, 0, time.UTC)))
		Expect(ranges[0].End).To(Equal(time.Date(2020, 5, 26, 0, 0, 0, 0, time.UTC)))
	})
})

var _ = Describe("EODArchive.TrackableRanges", func() {
	It("drops a historical range whose duration is below the asset-creation minimum", func() {
		root := GinkgoT().TempDir()

		// A 1-day historical fragment (Oct 22-23) followed by a 61-day
		// gap and then a long run with bars at most 60 days apart so the
		// run stays one range. The fragment is the ABV.C noise we want
		// filtered; the long run is what survives.
		writeArchiveDayFile(root, time.Date(2003, 10, 22, 0, 0, 0, 0, time.UTC), []string{"ABV/C"})
		writeArchiveDayFile(root, time.Date(2003, 10, 23, 0, 0, 0, 0, time.UTC), []string{"ABV/C"})
		writeArchiveDayFile(root, time.Date(2003, 12, 23, 0, 0, 0, 0, time.UTC), []string{"ABV/C"})
		writeArchiveDayFile(root, time.Date(2004, 2, 21, 0, 0, 0, 0, time.UTC), []string{"ABV/C"})
		writeArchiveDayFile(root, time.Date(2004, 4, 21, 0, 0, 0, 0, time.UTC), []string{"ABV/C"})
		writeArchiveDayFile(root, time.Date(2004, 6, 20, 0, 0, 0, 0, time.UTC), []string{"ABV/C"})
		// A later bar for a different ticker pushes coverage end past
		// the long-run end, so the long run qualifies on duration
		// alone rather than being tail-protected.
		writeArchiveDayFile(root, time.Date(2010, 1, 4, 0, 0, 0, 0, time.UTC), []string{"OTHER"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		// Raw Ranges sees both: the 1-day fragment and the long run.
		raw := archive.Ranges("ABV/C")
		Expect(raw).To(HaveLen(2))

		// TrackableRanges drops the 1-day fragment and keeps the long run.
		got := archive.TrackableRanges("ABV/C")
		Expect(got).To(HaveLen(1))
		Expect(got[0].Start).To(Equal(time.Date(2003, 12, 23, 0, 0, 0, 0, time.UTC)))
		Expect(got[0].End).To(Equal(time.Date(2004, 6, 20, 0, 0, 0, 0, time.UTC)))
	})

	It("keeps a short range whose End matches the archive's coverage end (new-listing tail)", func() {
		root := GinkgoT().TempDir()

		// A long historical range for one ticker establishes the
		// archive's coverage end on 2020-06-30. A second ticker first
		// trades on 2020-06-15 and is still trading on coverage end —
		// 16 days, well under the 60-day minimum, but tail-protected.
		writeArchiveDayFile(root, time.Date(2010, 1, 4, 0, 0, 0, 0, time.UTC), []string{"OLD"})
		writeArchiveDayFile(root, time.Date(2020, 6, 30, 0, 0, 0, 0, time.UTC), []string{"OLD", "NEW"})
		writeArchiveDayFile(root, time.Date(2020, 6, 15, 0, 0, 0, 0, time.UTC), []string{"NEW"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		got := archive.TrackableRanges("NEW")
		Expect(got).To(HaveLen(1))
		Expect(got[0].Start).To(Equal(time.Date(2020, 6, 15, 0, 0, 0, 0, time.UTC)))
		Expect(got[0].End).To(Equal(time.Date(2020, 6, 30, 0, 0, 0, 0, time.UTC)))
	})

	It("keeps a historical range whose duration meets the minimum", func() {
		root := GinkgoT().TempDir()

		// A historical range with internal bars at most 60 days apart
		// (Jan 1 → Feb 25 → Mar 6 = 64-day single range), followed by a
		// 10-year gap that opens a second range. Both qualify: the
		// first on duration alone, the second is tail-protected
		// because its End matches the archive's coverage end.
		writeArchiveDayFile(root, time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC), []string{"OK"})
		writeArchiveDayFile(root, time.Date(2010, 2, 25, 0, 0, 0, 0, time.UTC), []string{"OK"})
		writeArchiveDayFile(root, time.Date(2010, 3, 6, 0, 0, 0, 0, time.UTC), []string{"OK"})
		writeArchiveDayFile(root, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), []string{"OK"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		got := archive.TrackableRanges("OK")
		Expect(got).To(HaveLen(2))
		Expect(got[0].Start).To(Equal(time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)))
		Expect(got[0].End).To(Equal(time.Date(2010, 3, 6, 0, 0, 0, 0, time.UTC)))
		Expect(got[1].End).To(Equal(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
	})

	It("returns nil for a ticker the archive has no bars for", func() {
		root := GinkgoT().TempDir()

		writeArchiveDayFile(root, time.Date(2020, 5, 22, 0, 0, 0, 0, time.UTC), []string{"AAPL"})

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		Expect(archive.TrackableRanges("ZZZ")).To(BeNil())
	})
})

var _ = Describe("EODArchive index sidecar", func() {
	It("writes the parquet index file at the archive root after a successful scan", func() {
		root := GinkgoT().TempDir()
		writeArchiveDayFile(root, time.Date(2020, 5, 22, 0, 0, 0, 0, time.UTC), []string{"AAPL"})

		_, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		_, statErr := os.Stat(filepath.Join(root, "eod_date_index.parquet"))
		Expect(statErr).NotTo(HaveOccurred())
	})

	It("reuses the cached index on a second load and only reads files newer than last_indexed_date", func() {
		root := GinkgoT().TempDir()

		writeArchiveDayFile(root, time.Date(2020, 5, 22, 0, 0, 0, 0, time.UTC), []string{"AAPL"})

		first, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.TickerCount()).To(Equal(1))

		// Add a newer day with a new ticker; the index should pick it
		// up incrementally on the next load.
		writeArchiveDayFile(root, time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC), []string{"AAPL", "MSFT"})

		// Clear the process-wide cache so we actually re-read from disk.
		eodArchiveMu.Lock()
		delete(eodArchiveCache, root)
		eodArchiveMu.Unlock()

		second, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		Expect(second.TickerCount()).To(Equal(2))

		msftRanges := second.Ranges("MSFT")
		Expect(msftRanges).To(HaveLen(1))
		Expect(msftRanges[0].Start).To(Equal(time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)))
	})

	It("falls back to a full rescan when the index file is unreadable", func() {
		root := GinkgoT().TempDir()
		writeArchiveDayFile(root, time.Date(2020, 5, 22, 0, 0, 0, 0, time.UTC), []string{"AAPL"})

		// Plant a corrupt index ahead of time (not a valid parquet file).
		bogus := []byte("not a parquet file")
		Expect(os.WriteFile(filepath.Join(root, "eod_date_index.parquet"), bogus, 0o644)).To(Succeed())

		archive, err := LoadEODArchive(root)
		Expect(err).NotTo(HaveOccurred())

		// AAPL is present, proving we rescanned the per-day parquet
		// rather than blindly trusting the corrupt index.
		_, _, ok := archive.Lookup("AAPL")
		Expect(ok).To(BeTrue())
	})
})
