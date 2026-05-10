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

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/parquet-go/parquet-go"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("Flat-file parquet backup", func() {
	Describe("subscriptionBackupSlug", func() {
		It("combines a slugified name with a 5-char id suffix and uses underscores", func() {
			id := uuid.MustParse("3a85a000-0000-0000-0000-000000000000")
			sub := &library.Subscription{Name: "Massive EOD", ID: id}

			Expect(subscriptionBackupSlug(sub)).To(Equal("massive_eod_3a85a"))
		})

		It("preserves uniqueness across subscriptions with the same name", func() {
			a := &library.Subscription{Name: "EOD", ID: uuid.MustParse("11111111-0000-0000-0000-000000000000")}
			b := &library.Subscription{Name: "EOD", ID: uuid.MustParse("22222222-0000-0000-0000-000000000000")}

			Expect(subscriptionBackupSlug(a)).NotTo(Equal(subscriptionBackupSlug(b)))
		})
	})

	Describe("writeCorporateActionsBackup", func() {
		It("buckets items by year and writes one parquet per year", func() {
			tmp, err := os.MkdirTemp("", "massive-ca-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, tmp)

			rows := []massiveSplit{
				{ID: "a", Ticker: "AAA", ExecutionDate: "2024-03-01", SplitFrom: 1, SplitTo: 2},
				{ID: "b", Ticker: "AAA", ExecutionDate: "2024-08-15", SplitFrom: 1, SplitTo: 3},
				{ID: "c", Ticker: "BBB", ExecutionDate: "2025-01-10", SplitFrom: 1, SplitTo: 4},
			}

			Expect(writeCorporateActionsBackup(tmp, rows, splitYear)).To(Succeed())

			for _, name := range []string{"2024.parquet", "2025.parquet"} {
				_, err := os.Stat(filepath.Join(tmp, name))
				Expect(err).NotTo(HaveOccurred(), "missing file %s", name)
			}

			got2024, err := parquet.ReadFile[massiveSplit](filepath.Join(tmp, "2024.parquet"))
			Expect(err).NotTo(HaveOccurred())
			Expect(got2024).To(HaveLen(2))

			got2025, err := parquet.ReadFile[massiveSplit](filepath.Join(tmp, "2025.parquet"))
			Expect(err).NotTo(HaveOccurred())
			Expect(got2025).To(HaveLen(1))
			Expect(got2025[0].Ticker).To(Equal("BBB"))
		})

		It("returns nil and does not create the directory when items is empty", func() {
			tmp, err := os.MkdirTemp("", "massive-ca-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, tmp)

			Expect(writeCorporateActionsBackup(filepath.Join(tmp, "missing"), []massiveSplit{}, splitYear)).To(Succeed())

			_, err = os.Stat(filepath.Join(tmp, "missing"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("groups records with unparseable dates into 0000.parquet", func() {
			tmp, err := os.MkdirTemp("", "massive-ca-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, tmp)

			rows := []massiveDividend{
				{ID: "x", Ticker: "AAA", ExDividendDate: "2024-05-01", CashAmount: 0.5},
				{ID: "y", Ticker: "BBB", ExDividendDate: "", CashAmount: 0.25}, // bad date
			}

			Expect(writeCorporateActionsBackup(tmp, rows, dividendYear)).To(Succeed())

			_, err = os.Stat(filepath.Join(tmp, "2024.parquet"))
			Expect(err).NotTo(HaveOccurred())

			_, err = os.Stat(filepath.Join(tmp, "0000.parquet"))
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("backupPathFor", func() {
		It("groups files by year under baseDir with the date as the filename", func() {
			d := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
			got := backupPathFor("/var/backup", d)
			Expect(got).To(Equal(filepath.FromSlash("/var/backup/2026/2026-05-08.parquet")))
		})

		It("uses a 4-digit year directory regardless of month", func() {
			d := time.Date(2003, 9, 9, 0, 0, 0, 0, time.UTC)
			got := backupPathFor("/var/backup/sub_x", d)
			Expect(got).To(Equal(filepath.FromSlash("/var/backup/sub_x/2003/2003-09-09.parquet")))
		})
	})

	Describe("backupExists", func() {
		It("returns false when the file is absent", func() {
			tmp, err := os.MkdirTemp("", "massive-backup-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, tmp)

			ok, err := backupExists(filepath.Join(tmp, "missing.parquet"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})

		It("returns true when the file exists", func() {
			tmp, err := os.MkdirTemp("", "massive-backup-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, tmp)

			path := filepath.Join(tmp, "present.parquet")
			Expect(os.WriteFile(path, []byte("x"), 0o600)).To(Succeed())

			ok, err := backupExists(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
		})
	})

	Describe("writeFlatFileBackup", func() {
		It("writes a parquet file with every CSV column and round-trips the rows", func() {
			tmp, err := os.MkdirTemp("", "massive-backup-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, tmp)

			rows := []aggRow{
				{Ticker: "A", Volume: 100, Open: 118.7, Close: 118.7, High: 118.7, Low: 118.7, WindowStart: 1778238240000000000, Transactions: 1},
				{Ticker: "A", Volume: 519.5, Open: 117.92, Close: 117.91, High: 117.92, Low: 117.91, WindowStart: 1778246940000000000, Transactions: 8},
			}

			dest := filepath.Join(tmp, "us_stocks_sip/day_aggs_v1/2026/05/2026-05-08.parquet")
			Expect(writeFlatFileBackup(dest, rows)).To(Succeed())

			info, err := os.Stat(dest)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Size()).To(BeNumerically(">", 0))

			// Confirm the temp file was renamed away cleanly.
			_, err = os.Stat(dest + ".tmp")
			Expect(os.IsNotExist(err)).To(BeTrue())

			// Round-trip read.
			got, err := parquet.ReadFile[aggRow](dest)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(len(rows)))
			Expect(got[0].Ticker).To(Equal("A"))
			Expect(got[0].Volume).To(Equal(100.0))
			Expect(got[0].WindowStart).To(Equal(int64(1778238240000000000)))
			Expect(got[1].Transactions).To(Equal(int64(8)))
		})

		It("creates intermediate directories under baseDir", func() {
			tmp, err := os.MkdirTemp("", "massive-backup-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, tmp)

			dest := filepath.Join(tmp, "deep/nested/path/file.parquet")
			Expect(writeFlatFileBackup(dest, []aggRow{{Ticker: "A", Volume: 1}})).To(Succeed())

			_, err = os.Stat(dest)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
