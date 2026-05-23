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

		It("keeps Massive's listing on the earliest lifecycle and clears it for later siblings that share the same value", func() {
			// BBI-style: Massive returns 1993-03-16 as the list_date
			// for both the early lifecycle and the later lifecycle.
			// The earlier lifecycle (lower EOD first bar) keeps it;
			// later siblings fall through to their own EOD first bar.
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

			Expect(got[0].ListingDate).To(Equal(d("1993-03-16")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_listing_date"))
			Expect(got[0].DelistingDate).To(Equal(d("2010-07-07")))

			Expect(got[1].ListingDate).To(Equal(d("2019-09-03")))
			Expect(got[1].ListingSource).To(Equal("massive_eod_archive_first_bar"))
			Expect(got[1].DelistingDate).To(Equal(d("2022-09-08")))

			Expect(got[0].DelistingDate.Before(got[1].ListingDate)).To(BeTrue())
		})

		It("keeps Massive's listing on the earliest lifecycle for a same-CIK bankruptcy gap (DAL-style)", func() {
			// DAL: pre-bankruptcy (1967-2005) and post-bankruptcy
			// (2007-now) share Massive list_date 1967-07-03. Earliest
			// lifecycle (lower EOD first bar) keeps it; the post-
			// bankruptcy lifecycle falls through to its own EOD
			// first bar.
			historical := DateCandidates{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveReferenceListingDate:    d("1967-07-03"),
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2005-10-12"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
				SECEarliestFilingMatchingForm:  d("1994-01-27"),
			}

			modern := DateCandidates{
				AssetType:                             data.CommonStock,
				Active:                                true,
				MassiveReferenceListingDate:           d("1967-07-03"),
				MassiveEODArchiveFirstBar:             d("2007-05-03"),
				MassiveEODArchiveLastBar:              d("2026-05-15"),
				MassiveEODArchiveCoverageStart:        d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:          d("2026-05-08"),
				MassiveEODArchivePreviousLifecycleEnd: d("2005-10-12"),
				SECEarliestFilingMatchingForm:         d("1994-01-27"),
			}

			got := AssignDatesForTicker(silentLogger(), []DateCandidates{historical, modern}, calendarPreviousDay)

			Expect(got).To(HaveLen(2))

			Expect(got[0].ListingDate).To(Equal(d("1967-07-03")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_listing_date"))
			Expect(got[0].DelistingDate).To(Equal(d("2005-10-13")))

			Expect(got[1].ListingDate).To(Equal(d("2007-05-03")))
			Expect(got[1].ListingSource).To(Equal("massive_eod_archive_first_bar"))
			Expect(got[1].DelistingDate.IsZero()).To(BeTrue())
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

		It("returns zero listing date only when every candidate, including ValidFor, is missing", func() {
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

		It("falls back to ValidFor when every other signal is missing", func() {
			// Sparse-Massive case: a long-delisted ticker that
			// Massive's reference no longer returns a list_date for,
			// has no EOD archive coverage (the EOD subscription is
			// unconfigured or the archive load failed), no walk
			// window entry under any index, and no CIK so SEC cannot
			// help either. The observation's ValidFor is the only
			// remaining signal that the asset existed; the algorithm
			// must use it rather than emit null.
			candidates := []DateCandidates{{
				AssetType: data.CommonStock,
				Active:    false,
				ValidFor:  d("2018-04-12"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2018-04-12")))
			Expect(got[0].ListingSource).To(Equal("valid_for"))
		})

		It("rejects MassiveReferenceListingDate when it predates the previous EOD lifecycle's end (ticker reassignment)", func() {
			// OSG's Octave Specialty Group case: ticker OSG was held
			// by Overseas Shipholding through 2024-07-09, then Octave
			// took the ticker and its first EOD bar is 2025-11-20.
			// Massive's per-ticker endpoint reports list_date
			// 1989-08-25 for the new entity — that is the original
			// ticker allocation date, not when Octave started trading
			// under OSG. Accepting it would put a 1989 listing on a
			// row whose entity did not exist on this ticker until
			// 2025. With the previous-lifecycle gate in place, the
			// algorithm rejects the 1989 value and falls through to
			// Octave's own EOD first bar.
			candidates := []DateCandidates{{
				AssetType:                             data.CommonStock,
				Active:                                true,
				MassiveReferenceListingDate:           d("1989-08-25"),
				MassiveEODArchiveFirstBar:             d("2025-11-20"),
				MassiveEODArchiveLastBar:              d("2026-05-15"),
				MassiveEODArchiveCoverageStart:        d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:          d("2026-05-15"),
				MassiveEODArchivePreviousLifecycleEnd: d("2024-07-09"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2025-11-20")))
			Expect(got[0].ListingSource).To(Equal("massive_eod_archive_first_bar"))
		})

		It("rejects MassiveReferenceListingDate when the ticker's first EOD bar is multiple years after Massive's claim and coverage was active during the gap (iShares Morningstar rebrand)", func() {
			// iShares Morningstar IMCG case: Massive returns list_date
			// 2004-06-28 for ticker IMCG, but IMCG's own EOD lifecycle
			// begins 2021-03-22 — the date the iShares Morningstar
			// family was rebranded from JK* tickers to IM/IL/IS*
			// tickers. The previous-lifecycle gate above does not fire
			// because the new ticker has no earlier lifecycle in the
			// archive under IMCG itself. The gap-tolerance gate
			// catches it: coverage starts 2003-09-10 (active during
			// the claimed 2004 listing), and the 17-year gap between
			// the claim and the first bar is far above the 365-day
			// tolerance.
			candidates := []DateCandidates{{
				AssetType:                      data.ETF,
				Active:                         true,
				MassiveReferenceListingDate:    d("2004-06-28"),
				MassiveEODArchiveFirstBar:      d("2021-03-22"),
				MassiveEODArchiveLastBar:       d("2026-05-15"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-15"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2021-03-22")))
			Expect(got[0].ListingSource).To(Equal("massive_eod_archive_first_bar"))
		})

		It("rejects MassiveReferenceListingDate for an OTC-to-listed transition where the entity predates coverage but the ticker's first bar is well inside coverage", func() {
			// BLFS-style case: Massive returns the entity's
			// existence date (1989-11-22) but the asset did not
			// reach a tracked exchange until 2014. The EOD archive
			// has continuous coverage from 2003-09-10 and the
			// first BLFS bar is 2014-03-26 — there are no ingest
			// gaps inside coverage, so the absence of bars from
			// 2003-09-10 to 2014-03-26 is evidence the ticker
			// was not tracked-exchange-listed during that span.
			// The gate fires even though Massive's claim predates
			// our coverage window.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveReferenceListingDate:    d("1989-11-22"),
				MassiveEODArchiveFirstBar:      d("2014-03-26"),
				MassiveEODArchiveLastBar:       d("2026-05-15"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-15"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2014-03-26")))
			Expect(got[0].ListingSource).To(Equal("massive_eod_archive_first_bar"))
		})

		It("accepts MassiveReferenceListingDate when it predates the archive's coverage start AND the first bar sits at the coverage edge (legitimately old asset)", func() {
			// MSA's case: list_date 1988-09-26 predates our 2003-09-10
			// coverage. The gap-tolerance gate must not fire because
			// the archive cannot speak to whether trading occurred
			// before its coverage window opened. Without that
			// guardrail we would discard valid listings for every
			// pre-2003 asset.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveReferenceListingDate:    d("1988-09-26"),
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2026-05-15"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-15"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("1988-09-26")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_listing_date"))
		})

		It("accepts MassiveReferenceListingDate when the gap to the first EOD bar is inside the tolerance (routine IPO lag)", func() {
			// A newly-listed asset whose first bar lands a few weeks
			// after the listing — common when archive ingestion lags
			// or when the IPO window has thin trading. The
			// gap-tolerance gate must not fire because the gap is
			// well below 365 days.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveReferenceListingDate:    d("2022-04-01"),
				MassiveEODArchiveFirstBar:      d("2022-04-15"),
				MassiveEODArchiveLastBar:       d("2026-05-15"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-15"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2022-04-01")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_listing_date"))
		})

		It("accepts MassiveReferenceListingDate when there is no previous EOD lifecycle (no reassignment)", func() {
			// MSA's case: single continuous EOD lifecycle. Massive's
			// list_date 1988-09-26 predates EOD coverage but the
			// entity has been continuously trading throughout — there
			// is no previous lifecycle to compare against and the
			// gate must not fire. The list_date is the correct
			// answer.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveReferenceListingDate:    d("1988-09-26"),
				MassiveEODArchiveFirstBar:      d("2003-09-10"),
				MassiveEODArchiveLastBar:       d("2026-05-15"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-15"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("1988-09-26")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_listing_date"))
		})

		It("uses massive_eod_archive_last_bar_edge for an inactive asset whose EOD last bar sits at the coverage edge", func() {
			// The inactive-only edge fallback exists so that
			// active=false rows always carry a delisted timestamp even
			// when the EOD last bar sits inside the buffer window of
			// the archive's coverage end and the normal candidate is
			// gated. The result is the last bar + 1 calendar day, the
			// same as the non-edge candidate.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         false,
				MassiveReferenceListingDate:    d("2018-04-10"),
				MassiveEODArchiveFirstBar:      d("2018-04-10"),
				MassiveEODArchiveLastBar:       d("2026-04-30"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].DelistingDate).To(Equal(d("2026-05-01")))
			Expect(got[0].DelistingSource).To(Equal("massive_eod_archive_last_bar_edge"))
		})

		It("uses massive_reference_walk_last_seen_edge for an inactive asset with only walk evidence at the walk window edge", func() {
			// When EOD coverage is missing and the walk last-seen
			// sits inside the buffer of walk_end, the high-confidence
			// walk_last_seen candidate is gated out. The inactive-only
			// edge fallback supplies last_seen + 1 calendar day so the
			// active=false row still ends with a delisted timestamp.
			candidates := []DateCandidates{{
				AssetType:                     data.CommonStock,
				Active:                        false,
				MassiveReferenceListingDate:   d("2003-09-15"),
				MassiveReferenceWalkFirstSeen: d("2003-09-15"),
				MassiveReferenceWalkLastSeen:  d("2026-05-01"),
				MassiveReferenceWalkStart:     d("2003-09-10"),
				MassiveReferenceWalkEnd:       d("2026-05-08"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].DelistingDate).To(Equal(d("2026-05-02")))
			Expect(got[0].DelistingSource).To(Equal("massive_reference_walk_last_seen_edge"))
		})

		It("returns zero delisting for an inactive asset with no EOD or walk evidence (no metadata-mtime fallback)", func() {
			// last_updated and ValidFor are metadata timestamps, not
			// trading signals, so they are not delisting candidates.
			// When no EOD bars and no walk evidence exist the algorithm
			// must return zero rather than stamping today's date — the
			// caller (delistedAssets) decides how to handle the gap.
			candidates := []DateCandidates{{
				AssetType:                   data.CommonStock,
				Active:                      false,
				MassiveReferenceListingDate: d("2010-03-15"),
				ValidFor:                    d("2026-05-15"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].DelistingDate.IsZero()).To(BeTrue())
			Expect(got[0].DelistingSource).To(Equal(""))
		})

		It("does not run the inactive-only edge fallbacks for an active asset", func() {
			// Active assets are expected to have an open-ended (null)
			// delisting. Even with an EOD last bar at the coverage
			// edge, the edge fallback must not fire because the rule
			// it backstops (active=false implies delisted set) does
			// not apply.
			candidates := []DateCandidates{{
				AssetType:                      data.CommonStock,
				Active:                         true,
				MassiveReferenceListingDate:    d("2018-04-10"),
				MassiveEODArchiveFirstBar:      d("2018-04-10"),
				MassiveEODArchiveLastBar:       d("2026-04-30"),
				MassiveEODArchiveCoverageStart: d("2003-09-10"),
				MassiveEODArchiveCoverageEnd:   d("2026-05-08"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].DelistingDate.IsZero()).To(BeTrue())
			Expect(got[0].DelistingSource).To(Equal(""))
		})

		It("prefers walk first-seen edge over ValidFor when both are available", func() {
			// ValidFor is the absolute lowest priority: any signal
			// older than the observation is preferred. Walk
			// first-seen at the walk-start edge is the next-lowest
			// real signal, so the algorithm must pick it ahead of
			// ValidFor.
			candidates := []DateCandidates{{
				AssetType:                     data.CommonStock,
				Active:                        false,
				MassiveReferenceWalkFirstSeen: d("2003-09-10"),
				MassiveReferenceWalkLastSeen:  d("2010-06-30"),
				MassiveReferenceWalkStart:     d("2003-09-10"),
				MassiveReferenceWalkEnd:       d("2026-05-08"),
				ValidFor:                      d("2018-04-12"),
			}}

			got := AssignDatesForTicker(silentLogger(), candidates, calendarPreviousDay)

			Expect(got).To(HaveLen(1))
			Expect(got[0].ListingDate).To(Equal(d("2003-09-10")))
			Expect(got[0].ListingSource).To(Equal("massive_reference_walk_first_seen_edge"))
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
