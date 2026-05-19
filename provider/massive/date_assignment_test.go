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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"

	"github.com/penny-vault/pvdata/data"
)

// d is a short helper for building ISO-date time values inside tests.
func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}

	return t
}

// calendarPreviousDay is a trading-day-naive fallback used by tests
// that do not depend on weekend / holiday behavior. Production
// wiring passes a function backed by the trading_days() SQL helper.
func calendarPreviousDay(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}

	return t.AddDate(0, 0, -1)
}

// silentLogger returns a zerolog.Logger that drops every event so
// tests do not pollute go test output with warn-level diagnostics.
func silentLogger() *zerolog.Logger {
	l := zerolog.Nop()
	return &l
}

var _ = Describe("AssignDatesForTicker", func() {
	Context("single asset with one usable source", func() {
		It("returns the Massive reference listing date when only that source has a value", func() {
			candidates := []DateCandidates{{
				AssetType:                     data.CommonStock,
				Active:                        false,
				MassiveReferenceListingDate:   d("2010-03-15"),
				MassiveReferenceDelistingDate: d("2018-12-01"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2010-03-15")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_listing_date"))
			Expect(got[0].DelistingDate).To(Equal(d("2018-12-01")))
			Expect(got[0].DelistingSource).To(Equal("massive_reference_delisting_date"))
		})

		It("falls back to the EOD first bar when Massive reference has no listing date", func() {
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveEODArchiveFirstBar:      d("2018-04-10"),
				MassiveEODArchiveCoverageStart: d("2010-01-01"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-01"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate).To(Equal(d("2018-04-10")))
			Expect(got[0].ListingSource).To(Equal("massive_eod_archive_first_bar"))
		})

		It("rejects the EOD first bar when it sits at the archive's coverage edge", func() {
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveEODArchiveFirstBar:      d("2010-01-05"),
				MassiveEODArchiveCoverageStart: d("2010-01-01"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-01"),
				SECEarliestFilingMatchingForm:  d("2009-07-22"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate).To(Equal(d("2009-07-22")))
			Expect(got[0].ListingSource).To(Equal("sec_earliest_filing_matching_form"))
		})
	})

	Context("priority order across sources for one asset", func() {
		It("prefers Massive reference listing over EOD, walk, and SEC when all are usable", func() {
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveReferenceListingDate:    d("2015-06-01"),
				MassiveEODArchiveFirstBar:      d("2015-06-15"),
				MassiveEODArchiveCoverageStart: d("2010-01-01"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-01"),
				MassiveReferenceWalkFirstSeen:  d("2015-06-20"),
				MassiveReferenceWalkStart:      d("2014-01-01"),
				MassiveReferenceWalkEnd:        d("2026-05-01"),
				SECEarliestFilingMatchingForm:  d("2015-03-30"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate).To(Equal(d("2015-06-01")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_listing_date"))
		})

		It("prefers the EOD first bar over walk first-seen and SEC when Massive reference is missing", func() {
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveEODArchiveFirstBar:      d("2015-06-15"),
				MassiveEODArchiveCoverageStart: d("2010-01-01"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-01"),
				MassiveReferenceWalkFirstSeen:  d("2015-06-20"),
				MassiveReferenceWalkStart:      d("2014-01-01"),
				MassiveReferenceWalkEnd:        d("2026-05-01"),
				SECEarliestFilingMatchingForm:  d("2015-03-30"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate).To(Equal(d("2015-06-15")))
			Expect(got[0].ListingSource).To(Equal("massive_eod_archive_first_bar"))
		})

		It("prefers walk first-seen over SEC when neither Massive reference nor EOD are usable", func() {
			candidates := []DateCandidates{{
				AssetType:                     data.CommonStock,
				Active:                        true,
				MassiveReferenceWalkFirstSeen: d("2015-06-20"),
				MassiveReferenceWalkStart:     d("2014-01-01"),
				MassiveReferenceWalkEnd:       d("2026-05-01"),
				SECEarliestFilingMatchingForm: d("2015-03-30"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate).To(Equal(d("2015-06-20")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_walk_first_seen"))
		})
	})

	Context("multi-asset ticker reuse (Alcoa-style)", func() {
		It("forces the predecessor's delisting to the trading day before the successor's listing", func() {
			predecessor := DateCandidates{
				AssetType:                   data.CommonStock,
				Active:                      false,
				MassiveReferenceListingDate: d("1994-02-03"),
			}

			successor := DateCandidates{
				AssetType:                   data.CommonStock,
				Active:                      true,
				MassiveReferenceListingDate: d("2016-10-18"),
			}

			got := AssignDatesForTicker(silentLogger(), []DateCandidates{predecessor, successor}, calendarPreviousDay)

			Expect(got).To(HaveLen(2))
			Expect(got[0].ListingDate).To(Equal(d("1994-02-03")))
			Expect(got[0].DelistingDate).To(Equal(d("2016-10-17")))
			Expect(got[0].DelistingSource).To(Equal("successor_boundary_minus_one_trading_day"))
			Expect(got[1].ListingDate).To(Equal(d("2016-10-18")))
			Expect(got[1].ListingSource).To(Equal("massive_reference_listing_date"))
		})

		It("preserves caller input order on the output regardless of internal sort", func() {
			successor := DateCandidates{
				AssetType:                   data.CommonStock,
				Active:                      true,
				MassiveReferenceListingDate: d("2016-10-18"),
			}

			predecessor := DateCandidates{
				AssetType:                   data.CommonStock,
				Active:                      false,
				MassiveReferenceListingDate: d("1994-02-03"),
			}

			got := AssignDatesForTicker(silentLogger(), []DateCandidates{successor, predecessor}, calendarPreviousDay)

			Expect(got).To(HaveLen(2))
			Expect(got[0].ListingDate).To(Equal(d("2016-10-18")))
			Expect(got[1].ListingDate).To(Equal(d("1994-02-03")))
			Expect(got[1].DelistingDate).To(Equal(d("2016-10-17")))
		})

		It("falls back to the EOD first bar at the coverage edge when every higher-priority candidate is rejected, so listed is never empty", func() {
			// MRH-style: Massive returns a misattributed CIK whose
			// SEC earliest filing (2011-11-10) postdates the EOD
			// first bar at coverage edge (2003-09-10). Strict gates
			// reject the SEC candidate because it would imply the
			// asset listed after a date we have proof it was
			// already trading. With no walk evidence and no Massive
			// reference list_date, the algorithm must still commit
			// to a listing date rather than leave it null — fall
			// back to the EOD first bar even at the coverage edge.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2015-07-31"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
				SECEarliestFilingMatchingForm:  d("2011-11-10"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate.IsZero()).To(BeFalse())
			Expect(got[0].ListingDate).To(Equal(d("2003-09-10")))
			Expect(got[0].ListingSource).To(Equal("massive_eod_archive_first_bar_edge"))
			Expect(got[0].DelistingDate).To(Equal(d("2015-08-01")))
		})

		It("treats an EOD first bar at the coverage edge as an upper bound on the listing date and rejects walk firstSeen that postdates it", func() {
			// ATE-style: the EOD archive shows a single lifecycle
			// whose first bar 2003-09-10 sits at the coverage start
			// (so the first bar is not usable as a listing-date value
			// directly). Walk firstSeen is 2006-10-02 because Massive
			// only assigned the current FIGI on that date. SEC's
			// earliest filing under the asset's CIK is 2002-03-01.
			// The first bar bound rejects walk's 2006-10-02 (which
			// would imply the asset listed after a date we already
			// have a bar for), and the algorithm falls through to
			// SEC's 2002-03-01 instead.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2016-04-21"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
				MassiveReferenceWalkFirstSeen:  d("2006-10-02"),
				MassiveReferenceWalkLastSeen:   d("2016-04-21"),
				MassiveReferenceWalkStart:      d("2003-09-10"),
				MassiveReferenceWalkEnd:        d("2026-05-08"),
				SECEarliestFilingMatchingForm:  d("2002-03-01"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2002-03-01")))
			Expect(got[0].ListingSource).To(Equal("sec_earliest_filing_matching_form"))
			Expect(got[0].DelistingDate).To(Equal(d("2016-04-22")))
			Expect(got[0].DelistingSource).To(Equal("massive_eod_archive_last_bar"))
		})

		It("treats an EOD last bar at the coverage edge as a lower bound on the delisting date and rejects Massive delisting that predates it", func() {
			// The EOD archive's last bar 2016-04-21 sits comfortably
			// before the coverage end 2026-05-08, so it is usable as
			// a delisting value. Massive incorrectly returns
			// 2010-01-01 as the delisting. The bound rejects 2010
			// because the asset clearly traded through 2016, and the
			// algorithm uses the EOD last bar instead.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveReferenceListingDate:    d("2003-09-10"),
				MassiveReferenceDelistingDate:  d("2010-01-01"),
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2016-04-21"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].DelistingDate).To(Equal(d("2016-04-22")))
			Expect(got[0].DelistingSource).To(Equal("massive_eod_archive_last_bar"))
		})

		It("rejects identical Massive listing dates across two same-ticker assets and falls back to lower-priority candidates", func() {
			// BBI-style: Massive returns the ticker's first-allocation
			// date 1993-03-16 as the list_date for both the early
			// holder (Blockbuster, 2003-2010 lifecycle) and the later
			// holder (Brickell Biotech, 2019-2022 lifecycle). Both
			// assets should reject that shared Massive value and fall
			// through to their own lower-priority candidates: the
			// early holder uses SEC because EOD sits at the coverage
			// edge for its lifecycle, and the later holder uses EOD
			// because its lifecycle is fully inside coverage.
			blockbuster := DateCandidates{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveReferenceListingDate:    d("1993-03-16"),
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2010-07-06"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
				SECEarliestFilingMatchingForm:  d("1999-05-06"),
			}

			brickell := DateCandidates{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveReferenceListingDate:    d("1993-03-16"),
				MassiveEODArchiveFirstBar:      d("2019-09-03"),
				MassiveEODArchiveLastBar:       d("2022-09-07"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
				SECEarliestFilingMatchingForm:  d("1994-02-11"),
			}

			got := AssignDatesForTicker(silentLogger(), []DateCandidates{blockbuster, brickell}, calendarPreviousDay)

			Expect(got).To(HaveLen(2))

			// Blockbuster: SEC earliest filing 1999-05-06 is the
			// next-priority listing candidate once the shared Massive
			// date is rejected. Delisting comes from the per-lifecycle
			// EOD last bar plus one calendar day.
			Expect(got[0].ListingDate).To(Equal(d("1999-05-06")))
			Expect(got[0].ListingSource).To(Equal("sec_earliest_filing_matching_form"))
			Expect(got[0].DelistingDate).To(Equal(d("2010-07-07")))

			// Brickell: EOD first bar 2019-09-03 is the next-priority
			// listing candidate once the shared Massive date is
			// rejected. Delisting comes from the per-lifecycle EOD
			// last bar plus one calendar day.
			Expect(got[1].ListingDate).To(Equal(d("2019-09-03")))
			Expect(got[1].ListingSource).To(Equal("massive_eod_archive_first_bar"))
			Expect(got[1].DelistingDate).To(Equal(d("2022-09-08")))

			// And the two windows do not overlap.
			Expect(got[0].DelistingDate.Before(got[1].ListingDate)).To(BeTrue())
		})

		It("produces non-overlapping windows when Massive reference would have created overlap", func() {
			// AA-style: Massive reference lists the successor on 2016-06-29
			// (the wrong value from our stale data) while predecessor's
			// delisting from Massive is 2016-10-17. Without the boundary
			// fix the two windows would overlap by four months.
			predecessor := DateCandidates{
				AssetType:                     data.CommonStock,
				Active:                        false,
				MassiveReferenceListingDate:   d("1994-02-03"),
				MassiveReferenceDelistingDate: d("2016-10-17"),
			}

			successor := DateCandidates{
				AssetType:                   data.CommonStock,
				Active:                      true,
				MassiveReferenceListingDate: d("2016-06-29"),
			}

			got := AssignDatesForTicker(silentLogger(), []DateCandidates{predecessor, successor}, calendarPreviousDay)

			// Boundary trusts the successor's Massive reference, so the
			// predecessor's delisting is moved to one trading day before
			// 2016-06-29 and the windows no longer overlap.
			Expect(got[0].DelistingDate).To(Equal(d("2016-06-28")))
			Expect(got[1].ListingDate).To(Equal(d("2016-06-29")))
			Expect(got[1].ListingDate.After(got[0].DelistingDate)).To(BeTrue())
		})

		It("preserves the later asset's delisting when its SEC earliest filing is older than the earlier asset's because the CIK is misattributed", func() {
			// Brickell-style: the later asset's CIK belongs to an
			// unrelated older entity (e.g. Massive tags Brickell
			// Biotech's BBI row with Arch Capital's CIK 0000819050,
			// which has 1994 filings). The earlier asset (Blockbuster)
			// has a more recent SEC earliest filing (1999). The OLD
			// sort by earliestKnownListing folded SEC's misattributed
			// date in and put Brickell ahead of Blockbuster, then
			// reconcileBoundary chopped Brickell's correct 2022
			// delisting back to one trading day before Blockbuster's
			// 1999 listing, after which enforceRules cleared it for
			// violating delisting > listing. The fix sorts by the
			// chosen listing instead, so Blockbuster precedes Brickell
			// and Brickell's 2022 delisting survives.
			blockbuster := DateCandidates{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2010-07-06"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
				SECEarliestFilingMatchingForm:  d("1999-05-06"),
			}

			brickell := DateCandidates{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveEODArchiveFirstBar:      d("2019-09-03"),
				MassiveEODArchiveLastBar:       d("2022-09-07"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
				SECEarliestFilingMatchingForm:  d("1994-02-11"),
			}

			got := AssignDatesForTicker(silentLogger(), []DateCandidates{brickell, blockbuster}, calendarPreviousDay)

			Expect(got).To(HaveLen(2))

			// Brickell (input index 0) keeps its 2019-09-03 listing
			// and 2022-09-08 delisting (EOD last bar + 1 calendar
			// day).
			Expect(got[0].ListingDate).To(Equal(d("2019-09-03")))
			Expect(got[0].DelistingDate).To(Equal(d("2022-09-08")))

			// Blockbuster (input index 1) keeps its SEC-derived
			// listing and EOD-derived delisting.
			Expect(got[1].ListingDate).To(Equal(d("1999-05-06")))
			Expect(got[1].DelistingDate).To(Equal(d("2010-07-07")))
		})
	})

	Context("hard constraints", func() {
		It("drops both Massive reference dates when they are not strictly ordered and uses the next-priority source", func() {
			candidates := []DateCandidates{{
				AssetType:                     data.CommonStock,
				Active:                        true,
				MassiveReferenceListingDate:   d("2010-03-15"),
				MassiveReferenceDelistingDate: d("2010-03-15"),
				SECEarliestFilingMatchingForm: d("2009-11-01"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate).To(Equal(d("2009-11-01")))
			Expect(got[0].ListingSource).To(Equal("sec_earliest_filing_matching_form"))
			Expect(got[0].DelistingDate.IsZero()).To(BeTrue())
			Expect(got[0].DelistingSource).To(Equal(""))
		})

		It("rejects a listing-date candidate that is not before the known delisting", func() {
			candidates := []DateCandidates{{
				AssetType:                     data.CommonStock,
				Active:                        false,
				MassiveReferenceListingDate:   d("2018-06-01"),
				MassiveReferenceDelistingDate: d("2015-01-01"),
				SECEarliestFilingMatchingForm: d("2014-09-30"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate).To(Equal(d("2014-09-30")))
			Expect(got[0].ListingSource).To(Equal("sec_earliest_filing_matching_form"))
		})

		It("returns zero listing date when no candidate is both usable and satisfies the constraints", func() {
			future := time.Now().AddDate(1, 0, 0)

			candidates := []DateCandidates{{
				AssetType:                   data.CommonStock,
				Active:                      true,
				MassiveReferenceListingDate: future,
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got[0].ListingDate.IsZero()).To(BeTrue())
			Expect(got[0].ListingSource).To(Equal(""))
		})
	})

	Context("empty input", func() {
		It("returns nil for an empty candidate slice", func() {
			got := AssignDatesForTicker(silentLogger(), nil, calendarPreviousDay)
			Expect(got).To(BeNil())
		})
	})
})

var _ = Describe("windowsOverlap", func() {
	Context("when both windows have both endpoints set", func() {
		It("reports overlap when ranges share any point in time", func() {
			a := &AssignedDates{ListingDate: d("2010-01-01"), DelistingDate: d("2015-01-01")}
			b := &AssignedDates{ListingDate: d("2014-01-01"), DelistingDate: d("2020-01-01")}
			Expect(windowsOverlap(a, b)).To(BeTrue())
		})

		It("reports non-overlap when the earlier window ends before the later begins", func() {
			a := &AssignedDates{ListingDate: d("2010-01-01"), DelistingDate: d("2012-01-01")}
			b := &AssignedDates{ListingDate: d("2014-01-01"), DelistingDate: d("2020-01-01")}
			Expect(windowsOverlap(a, b)).To(BeFalse())
		})
	})

	Context("when one or both windows have zero endpoints", func() {
		It("treats a zero delisting as forward-open and detects overlap with a later window", func() {
			open := &AssignedDates{ListingDate: d("2010-01-01")}
			later := &AssignedDates{ListingDate: d("2015-01-01"), DelistingDate: d("2020-01-01")}
			Expect(windowsOverlap(open, later)).To(BeTrue())
		})

		It("treats a zero listing as backward-open and detects overlap with an earlier window", func() {
			earlier := &AssignedDates{ListingDate: d("2010-01-01"), DelistingDate: d("2015-01-01")}
			open := &AssignedDates{DelistingDate: d("2020-01-01")}
			Expect(windowsOverlap(earlier, open)).To(BeTrue())
		})

		It("treats both-zero as fully open and overlapping with every sibling", func() {
			open := &AssignedDates{}
			sibling := &AssignedDates{ListingDate: d("2010-01-01"), DelistingDate: d("2015-01-01")}
			Expect(windowsOverlap(open, sibling)).To(BeTrue())
			Expect(windowsOverlap(sibling, open)).To(BeTrue())
		})
	})
})

var _ = Describe("AssignDatesForTicker overlap rule", func() {
	It("flags a fully-open sibling row as overlapping with a dated sibling", func() {
		dated := DateCandidates{
			Active:                        false,
			MassiveReferenceListingDate:   d("2011-01-20"),
			MassiveReferenceDelistingDate: d("2021-01-01"),
		}
		fullyOpen := DateCandidates{
			Active: true,
			// No date evidence anywhere — the kind of row that
			// previously slipped past the overlap check because
			// every endpoint was zero.
			AssetType: data.CommonStock,
		}

		got := AssignDatesForTicker(silentLogger(), []DateCandidates{dated, fullyOpen}, calendarPreviousDay)

		Expect(got).To(HaveLen(2))
		// The dated row keeps its dates; the fully-open row is
		// detected but the algorithm does not invent values for it.
		Expect(got[0].ListingDate).To(Equal(d("2011-01-20")))
		Expect(got[0].DelistingDate).To(Equal(d("2021-01-01")))
		Expect(got[1].ListingDate.IsZero()).To(BeTrue())
		Expect(got[1].DelistingDate.IsZero()).To(BeTrue())
	})
})

var _ = Describe("isLookbackRun", func() {
	It("returns false when walk window is unset (daily / live run)", func() {
		api := &massiveAssetFetcher{}
		Expect(api.isLookbackRun()).To(BeFalse())
	})

	It("returns false when walk span is within the default daily-update lookback", func() {
		api := &massiveAssetFetcher{
			walkStart: d("2026-05-01"),
			walkEnd:   d("2026-05-10"),
		}
		Expect(api.isLookbackRun()).To(BeFalse())
	})

	It("returns true when walk span exceeds the daily-update threshold", func() {
		api := &massiveAssetFetcher{
			walkStart: d("2024-01-01"),
			walkEnd:   d("2026-05-01"),
		}
		Expect(api.isLookbackRun()).To(BeTrue())
	})
})
