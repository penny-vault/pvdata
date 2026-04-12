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
package cmd

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("fundamentalFields", func() {
	It("has every entry present in the FundamentalsKey schema with the correct SQL type", func() {
		dt := data.DataTypes[data.FundamentalsKey]
		Expect(dt).NotTo(BeNil(), "FundamentalsKey data type should be registered")

		schema := dt.Schema

		for _, f := range fundamentalFields {
			var want string

			switch f.kind {
			case kindInt:
				want = fmt.Sprintf("%s BIGINT", f.column)
			case kindFloat:
				want = fmt.Sprintf("%s NUMERIC", f.column)
			}

			Expect(strings.Contains(schema, want)).To(BeTrue(),
				"field %q not found in schema with expected type (looking for %q)", f.column, want)
		}
	})
})

var _ = Describe("fieldByName", func() {
	It("returns a known int field", func() {
		f, ok := fieldByName("revenues")
		Expect(ok).To(BeTrue())
		Expect(f.kind).To(Equal(kindInt))
	})

	It("returns a known float field", func() {
		f, ok := fieldByName("eps")
		Expect(ok).To(BeTrue())
		Expect(f.kind).To(Equal(kindFloat))
	})

	It("reports ok=false for an unknown field", func() {
		_, ok := fieldByName("not_a_field")
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("valuesDiffer", func() {
	Context("null handling", func() {
		It("treats two nils as equal", func() {
			Expect(valuesDiffer(nil, nil, 0.0001, 0)).To(BeFalse())
		})

		It("treats nil vs non-nil as different in either order", func() {
			v := 5.0
			Expect(valuesDiffer(&v, nil, 0.0001, 0)).To(BeTrue())
			Expect(valuesDiffer(nil, &v, 0.0001, 0)).To(BeTrue())
		})
	})

	Context("numeric comparison", func() {
		It("returns false for exactly equal values", func() {
			a, b := 100.0, 100.0
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeFalse())
		})

		It("returns false when relative diff is within tolerance", func() {
			a, b := 1_000_000.0, 1_000_050.0 // 0.005% < 0.01%
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeFalse())
		})

		It("returns true when relative diff exceeds tolerance", func() {
			a, b := 1_000_000.0, 1_001_000.0 // 0.1% > 0.01%
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeTrue())
		})

		It("applies the absolute tolerance floor", func() {
			a, b := 0.0, 0.5
			Expect(valuesDiffer(&a, &b, 0.0001, 1.0)).To(BeFalse())
		})

		It("treats zero vs zero as equal", func() {
			a, b := 0.0, 0.0
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeFalse())
		})

		It("treats zero vs a small non-zero as different when abs-tol is zero", func() {
			a, b := 0.0, 0.0001
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeTrue())
		})
	})
})

var _ = Describe("discoverFundamentalsSubscriptions", func() {
	It("returns the sec and sharadar tables when exactly one of each exists", func() {
		subs := []*library.Subscription{
			{
				Name: "sec-fundamentals", Provider: "sec", Active: true,
				DataTypes:     []string{data.FundamentalsKey},
				DataTables:    []string{"sec_fundamentals_v1"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sec_fundamentals_v1"},
			},
			{
				Name: "sharadar-fundamentals", Provider: "sharadar", Active: true,
				DataTypes:     []string{data.FundamentalsKey},
				DataTables:    []string{"sharadar_fundamentals_v1"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sharadar_fundamentals_v1"},
			},
			{
				Name: "tiingo-eod", Provider: "tiingo", Active: true,
				DataTypes: []string{data.EODKey},
			},
		}

		secTbl, sharadarTbl, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).NotTo(HaveOccurred())
		Expect(secTbl).To(Equal("sec_fundamentals_v1"))
		Expect(sharadarTbl).To(Equal("sharadar_fundamentals_v1"))
	})

	It("errors when the sec subscription is missing", func() {
		subs := []*library.Subscription{
			{
				Name: "sharadar-fundamentals", Provider: "sharadar", Active: true,
				DataTypes:     []string{data.FundamentalsKey},
				DataTables:    []string{"sharadar_fundamentals_v1"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sharadar_fundamentals_v1"},
			},
		}

		_, _, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("sec"))
	})

	It("errors when multiple sec subscriptions match", func() {
		subs := []*library.Subscription{
			{
				Name: "sec-a", Provider: "sec", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"a"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "a"},
			},
			{
				Name: "sec-b", Provider: "sec", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"b"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "b"},
			},
			{
				Name: "sharadar-a", Provider: "sharadar", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"c"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "c"},
			},
		}

		_, _, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("multiple"))
	})

	It("ignores inactive subscriptions when picking a match", func() {
		subs := []*library.Subscription{
			{
				Name: "sec-old", Provider: "sec", Active: false,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"old"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "old"},
			},
			{
				Name: "sec-new", Provider: "sec", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"new"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "new"},
			},
			{
				Name: "sharadar", Provider: "sharadar", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"sh"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sh"},
			},
		}

		secTbl, _, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).NotTo(HaveOccurred())
		Expect(secTbl).To(Equal("new"))
	})
})

var _ = Describe("resolveCompareOptions", func() {
	It("applies defaults: all fields, empty dimensions, zero since/until, text format", func() {
		raw := rawCompareFlags{relTol: 0.0001, format: "text"}

		opts, err := resolveCompareOptions(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(opts.relTol).To(Equal(0.0001))
		Expect(opts.fields).To(HaveLen(len(fundamentalFields)))
		Expect(opts.dimensions).To(BeEmpty())
		Expect(opts.format).To(Equal("text"))
		Expect(opts.since.IsZero()).To(BeTrue())
		Expect(opts.until.IsZero()).To(BeTrue())
	})

	It("narrows the field set when --fields is supplied", func() {
		raw := rawCompareFlags{fields: []string{"revenues", "eps"}, format: "text"}

		opts, err := resolveCompareOptions(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(opts.fields).To(HaveLen(2))
		Expect(opts.fields[0].column).To(Equal("revenues"))
		Expect(opts.fields[1].column).To(Equal("eps"))
	})

	It("errors when --fields names an unknown column", func() {
		raw := rawCompareFlags{fields: []string{"not_a_field"}, format: "text"}
		_, err := resolveCompareOptions(raw)
		Expect(err).To(HaveOccurred())
	})

	It("errors when --since is not a valid date", func() {
		raw := rawCompareFlags{since: "not-a-date", format: "text"}
		_, err := resolveCompareOptions(raw)
		Expect(err).To(HaveOccurred())
	})

	It("errors when --format is unknown", func() {
		raw := rawCompareFlags{format: "xml"}
		_, err := resolveCompareOptions(raw)
		Expect(err).To(HaveOccurred())
	})

	It("uppercases dimension values", func() {
		raw := rawCompareFlags{dimensions: []string{"arq", "MRQ"}, format: "text"}

		opts, err := resolveCompareOptions(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(opts.dimensions).To(Equal([]string{"ARQ", "MRQ"}))
	})

	It("parses a valid --since and --until into time.Time", func() {
		raw := rawCompareFlags{since: "2023-01-01", until: "2023-12-31", format: "text"}

		opts, err := resolveCompareOptions(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(opts.since.Format("2006-01-02")).To(Equal("2023-01-01"))
		Expect(opts.until.Format("2006-01-02")).To(Equal("2023-12-31"))
	})
})

var _ = Describe("buildDateKeyQuery", func() {
	It("generates a union query with no args when no filters are set", func() {
		sql, args := buildDateKeyQuery("sec_fund", "sh_fund", compareOptions{})
		Expect(sql).To(ContainSubstring("FROM sec_fund"))
		Expect(sql).To(ContainSubstring("FROM sh_fund"))
		Expect(args).To(BeEmpty())
	})

	It("applies ticker/dimension/date filters to both subqueries", func() {
		since, err := time.Parse("2006-01-02", "2023-01-01")
		Expect(err).NotTo(HaveOccurred())

		opts := compareOptions{
			tickers:    []string{"AAPL"},
			dimensions: []string{"ARQ"},
			since:      since,
		}

		sql, args := buildDateKeyQuery("sec_fund", "sh_fund", opts)
		Expect(sql).To(ContainSubstring("ticker = ANY"))
		Expect(sql).To(ContainSubstring("dimension = ANY"))
		Expect(sql).To(ContainSubstring("date_key >="))
		// 3 filters applied once per subquery = 6 args.
		Expect(args).To(HaveLen(6))
	})
})

var _ = Describe("buildRowQuery", func() {
	It("emits the column list in the opts.fields order", func() {
		opts := compareOptions{fields: []fundamentalField{{"revenues", kindInt}, {"eps", kindFloat}}}
		sql := buildRowQuery("sec_fund", opts)
		Expect(sql).To(ContainSubstring("ticker, composite_figi, dimension, date_key, revenues, eps"))
		Expect(sql).To(HavePrefix("SELECT "))
		Expect(sql).To(ContainSubstring("FROM sec_fund"))
	})
})

var _ = Describe("buildRowQueryArgs", func() {
	It("yields just the date_key when no filters are configured", func() {
		dk, err := time.Parse("2006-01-02", "2023-03-31")
		Expect(err).NotTo(HaveOccurred())

		args := buildRowQueryArgs(compareOptions{}, dk)
		Expect(args).To(HaveLen(1))
		Expect(args[0]).To(BeAssignableToTypeOf(time.Time{}))
	})

	It("includes ticker and dimension filter args ahead of the date_key", func() {
		dk, err := time.Parse("2006-01-02", "2023-03-31")
		Expect(err).NotTo(HaveOccurred())

		args := buildRowQueryArgs(compareOptions{
			tickers:    []string{"AAPL"},
			dimensions: []string{"ARQ"},
		}, dk)
		Expect(args).To(HaveLen(3))
	})
})

var _ = Describe("diffRowSet", func() {
	fields := []fundamentalField{{"revenues", kindInt}, {"eps", kindFloat}}

	It("emits one diffRecord per differing field when only one field mismatches", func() {
		v1, v2 := 100.0, 200.0
		e1 := 1.5

		sec := []*fundamentalRow{{
			ticker: "AAPL", compositeFigi: "BBG000B9XRY4", dimension: "ARQ",
			values: []*float64{&v1, &e1},
		}}
		sh := []*fundamentalRow{{
			ticker: "AAPL", compositeFigi: "BBG000B9XRY4", dimension: "ARQ",
			values: []*float64{&v2, &e1},
		}}

		recs := diffRowSet(sec, sh, fields, 0.0001, 0)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0].field).To(Equal("revenues"))
		Expect(*recs[0].secValue).To(Equal(100.0))
		Expect(*recs[0].sharadarValue).To(Equal(200.0))
	})

	It("reports missing-in-sharadar when a sec row has no match", func() {
		oneField := []fundamentalField{{"revenues", kindInt}}
		v := 100.0
		sec := []*fundamentalRow{{ticker: "AAPL", compositeFigi: "BBG1", dimension: "ARQ", values: []*float64{&v}}}

		recs := diffRowSet(sec, nil, oneField, 0.0001, 0)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0].kind).To(Equal(diffMissingShar))
	})

	It("reports missing-in-sec when a sharadar row has no match", func() {
		oneField := []fundamentalField{{"revenues", kindInt}}
		v := 100.0
		sh := []*fundamentalRow{{ticker: "AAPL", compositeFigi: "BBG1", dimension: "ARQ", values: []*float64{&v}}}

		recs := diffRowSet(nil, sh, oneField, 0.0001, 0)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0].kind).To(Equal(diffMissingSec))
	})

	It("emits no diffs when rows are identical", func() {
		oneField := []fundamentalField{{"revenues", kindInt}}
		v := 100.0
		sec := []*fundamentalRow{{ticker: "AAPL", compositeFigi: "BBG1", dimension: "ARQ", values: []*float64{&v}}}
		sh := []*fundamentalRow{{ticker: "AAPL", compositeFigi: "BBG1", dimension: "ARQ", values: []*float64{&v}}}

		recs := diffRowSet(sec, sh, oneField, 0.0001, 0)
		Expect(recs).To(BeEmpty())
	})

	It("emits one record per differing field when multiple fields mismatch", func() {
		rev1, rev2 := 100.0, 200.0
		eps1, eps2 := 1.0, 2.0

		sec := []*fundamentalRow{{ticker: "AAPL", compositeFigi: "BBG1", dimension: "ARQ", values: []*float64{&rev1, &eps1}}}
		sh := []*fundamentalRow{{ticker: "AAPL", compositeFigi: "BBG1", dimension: "ARQ", values: []*float64{&rev2, &eps2}}}

		recs := diffRowSet(sec, sh, fields, 0.0001, 0)
		Expect(recs).To(HaveLen(2))
	})
})

var _ = Describe("textDiffWriter", func() {
	mustPtr := func(v float64) *float64 { return &v }

	It("renders a field diff with ticker/figi/date_key/dimension and values", func() {
		dk, err := time.Parse("2006-01-02", "2023-03-31")
		Expect(err).NotTo(HaveOccurred())

		rec := diffRecord{
			kind:          diffField,
			ticker:        "AAPL",
			compositeFigi: "BBG000B9XRY4",
			dimension:     "ARQ",
			dateKey:       dk,
			field:         "revenues",
			secValue:      mustPtr(100),
			sharadarValue: mustPtr(200),
		}

		var buf bytes.Buffer

		w := newTextDiffWriter(&buf)
		Expect(w.Write(rec)).To(Succeed())
		Expect(w.Close()).To(Succeed())

		out := buf.String()
		for _, want := range []string{"AAPL", "BBG000B9XRY4", "2023-03-31", "ARQ", "revenues", "100", "200"} {
			Expect(out).To(ContainSubstring(want))
		}
	})

	It("renders a missing-in-sharadar record", func() {
		dk, err := time.Parse("2006-01-02", "2023-03-31")
		Expect(err).NotTo(HaveOccurred())

		rec := diffRecord{
			kind: diffMissingShar, ticker: "AAPL", compositeFigi: "BBG1",
			dimension: "ARQ", dateKey: dk,
		}

		var buf bytes.Buffer

		w := newTextDiffWriter(&buf)
		Expect(w.Write(rec)).To(Succeed())
		Expect(w.Close()).To(Succeed())
		Expect(buf.String()).To(ContainSubstring("missing in sharadar"))
	})
})

var _ = Describe("csvDiffWriter", func() {
	mustPtr := func(v float64) *float64 { return &v }

	It("emits a header row when closed with no records", func() {
		var buf bytes.Buffer

		w := newCSVDiffWriter(&buf)
		Expect(w.Close()).To(Succeed())

		reader := csv.NewReader(&buf)
		row, err := reader.Read()
		Expect(err).NotTo(HaveOccurred())

		want := []string{"ticker", "composite_figi", "dimension", "date_key", "kind", "field", "sec_value", "sharadar_value", "abs_diff", "rel_diff"}
		Expect(row).To(Equal(want))
	})

	It("emits a field-diff row with expected columns", func() {
		dk, err := time.Parse("2006-01-02", "2023-03-31")
		Expect(err).NotTo(HaveOccurred())

		rec := diffRecord{
			kind:          diffField,
			ticker:        "AAPL",
			compositeFigi: "BBG000B9XRY4",
			dimension:     "ARQ",
			dateKey:       dk,
			field:         "revenues",
			secValue:      mustPtr(100),
			sharadarValue: mustPtr(200),
		}

		var buf bytes.Buffer

		w := newCSVDiffWriter(&buf)
		Expect(w.Write(rec)).To(Succeed())
		Expect(w.Close()).To(Succeed())

		reader := csv.NewReader(&buf)
		_, _ = reader.Read() // header

		row, err := reader.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(row[0]).To(Equal("AAPL"))
		Expect(row[1]).To(Equal("BBG000B9XRY4"))
		Expect(row[4]).To(Equal("diff"))
		Expect(row[5]).To(Equal("revenues"))
		Expect(row[6]).To(Equal("100"))
		Expect(row[7]).To(Equal("200"))
	})
})

// Compile-time check that both writers implement the diffWriter interface.
var (
	_ diffWriter = (*textDiffWriter)(nil)
	_ diffWriter = (*csvDiffWriter)(nil)
)
