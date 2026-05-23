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
	"fmt"
	"sort"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog"
)

// edgeBufferDays is how many calendar days a date must sit inside its
// coverage window before that date is trusted as a real lifecycle
// boundary rather than the edge of our data. Sized to absorb the
// longest market closures we expect (Thanksgiving Thursday-Sunday,
// Christmas/NYE clusters when a holiday falls on a Friday, Hurricane
// Sandy in October 2012).
const edgeBufferDays = 14

// massiveListDateGapTolerance is how long Massive's reference list_date
// is allowed to predate the ticker's own EOD first bar before the
// algorithm rejects the Massive value. Sized so routine IPO-to-first-
// bar lags and brief data outages do not trip the gate, but multi-year
// gaps (the iShares Morningstar JK*/IM*-IL*-IS* rebrand returns
// list_date 2004-06-28 on tickers that first traded 2021-03-22) do.
const massiveListDateGapTolerance = 365 * 24 * time.Hour

// DateCandidates collects the per-asset inputs the date-assignment
// algorithm consumes when choosing a listing date and a delisting date
// for one asset that belongs to a ticker. The struct is self-contained:
// every value the algorithm needs is here, so the algorithm itself is
// a pure function over a slice of these. Unset date fields are the zero
// time.Time (use IsZero() to test), and source strings on the output
// match the field names below in snake_case so the JSONB lineage column
// records the source unambiguously.
type DateCandidates struct {
	// Asset identity and lifecycle state.
	AssetType data.AssetType
	Active    bool

	// Massive's per-ticker reference endpoint values for this asset.
	MassiveReferenceListingDate   time.Time
	MassiveReferenceDelistingDate time.Time

	// Historical walk of Massive's reference endpoint. FirstSeen /
	// LastSeen are the earliest and latest dates the reference endpoint
	// returned this asset across the walk. WalkStart / WalkEnd bracket
	// the walk window so the algorithm can check whether FirstSeen and
	// LastSeen sit safely inside it.
	MassiveReferenceWalkFirstSeen time.Time
	MassiveReferenceWalkLastSeen  time.Time
	MassiveReferenceWalkStart     time.Time
	MassiveReferenceWalkEnd       time.Time

	// Massive EOD archive walk results for this asset's bucket. FirstBar
	// is the earliest bar attributed to this asset; LastBar is the
	// latest. CoverageStart and CoverageEnd are the earliest and latest
	// dates present anywhere in the archive across all assets.
	// PreviousLifecycleEnd is the end date of the EOD lifecycle that
	// immediately precedes this asset's lifecycle for the ticker, or
	// zero when this asset is in the earliest lifecycle. It bounds
	// MassiveReferenceListingDate from below: a Massive list_date
	// older than the previous lifecycle's end is from a different
	// (earlier) context on the same ticker — Octave Specialty Group
	// took ticker OSG in late 2025 but Massive reports list_date
	// 1989-08-25, which predates the previous Overseas Shipholding
	// lifecycle that ended 2024-07-09.
	MassiveEODArchiveFirstBar             time.Time
	MassiveEODArchiveLastBar              time.Time
	MassiveEODArchiveCoverageStart        time.Time
	MassiveEODArchiveCoverageEnd          time.Time
	MassiveEODArchivePreviousLifecycleEnd time.Time

	// SEC EDGAR signals. FormPrefix is the registration form used for
	// this asset type ("N-1A" for ETF / MutualFund, "N-2" for CEF, ""
	// for non-fund types where we use earliest filing of any kind).
	// EarliestFilingMatchingForm is the resulting date.
	SECFormPrefix                 string
	SECEarliestFilingMatchingForm time.Time

	// ValidFor is the as-of date at which the caller observed this
	// asset record. It is the absolute lower-bound signal: the asset
	// demonstrably existed under this ticker on this date, so the
	// listing date must be on or before it. Used only as the final
	// fallback when every higher-priority candidate is unavailable, so
	// that listed never lands in the database as null. Typically equals
	// asset.ValidFor: the historical observation date during a walk,
	// or time.Now() during a daily run.
	ValidFor time.Time
}

// AssignedDates is the algorithm's per-asset result: the chosen listing
// and delisting dates and the source name each came from. Source names
// match the DateCandidates field names in snake_case (or "" when the
// algorithm could not produce a value).
type AssignedDates struct {
	ListingDate     time.Time
	ListingSource   string
	DelistingDate   time.Time
	DelistingSource string
}

// Lineage is one provenance entry for a derived field on an asset.
// Two entries are emitted per asset by AssignDatesForTicker (one for
// listed, one for delisted); other enrichers can append their own
// entries for fields they own. Serialized into the JSONB `lineage`
// column on the asset table.
type Lineage struct {
	Field           string `json:"field"`
	Source          string `json:"source"`
	SoftwareVersion string `json:"software_version"`
}

// previousTradingDayFn returns the trading day immediately before t,
// honoring weekends and market holidays. Injected so the pure
// algorithm can be unit-tested without a database; production wiring
// passes a function backed by the trading_days() SQL helper.
type previousTradingDayFn func(t time.Time) time.Time

// AssignDatesForTicker is the cross-asset entry point. It receives one
// DateCandidates per asset that shares a ticker (one entry for the
// common single-asset case, two or more for ticker reuse like Alcoa),
// and returns one AssignedDates per input asset in the same order.
// The function enforces three immutable rules across the result:
// (1) no two assets sharing the ticker have overlapping (listing,
// delisting] windows; (2) each asset's delisting date is strictly
// after its listing date; (3) any asset whose Active flag is false
// must have a non-zero delisting date.
func AssignDatesForTicker(
	logger *zerolog.Logger,
	candidates []DateCandidates,
	previousTradingDay previousTradingDayFn,
) []AssignedDates {
	if len(candidates) == 0 {
		return nil
	}

	// Preserve the caller's input ordering on the output by recording
	// each candidate's original index before any internal sort.
	indexed := make([]indexedCandidate, len(candidates))
	for i, c := range candidates {
		indexed[i] = indexedCandidate{index: i, candidates: c}
	}

	// Per-asset listing and delisting choice, before cross-asset
	// reconciliation. Each asset uses its own candidates with the
	// priority order applied.
	assignments := make([]AssignedDates, len(indexed))
	for i, ic := range indexed {
		assignments[i] = chooseDatesForAsset(ic.candidates)
	}

	// Reject Massive reference listing dates that, if accepted, would
	// make this asset's window overlap a sibling same-ticker asset's
	// window. The classic failure mode is Massive returning the
	// ticker's first-allocation date as the per-asset list_date for
	// every company that has ever held the ticker (e.g. BBI returns
	// 1993-03-16 for both Blockbuster's 2003-2010 lifecycle and
	// Brickell's 2019-2022 lifecycle). Affected assets re-run
	// chooseDatesForAsset with their MassiveReferenceListingDate
	// suppressed, falling through to the next priority candidate.
	rejectOverlappingMassiveListings(logger, indexed, assignments)

	// Sort by chosen listing date so predecessor precedes successor
	// for reconcileBoundary's adjacent-pair walk. Using the chosen
	// listing (the output of chooseDatesForAsset, post-rejection) is
	// the right signal: it is the algorithm's best estimate of when
	// the asset actually listed, with all the priority-order and
	// gate logic already applied. Sorting by earliestKnownListing
	// (which folded in SEC's earliest filing for the CIK) produced
	// wrong orderings when Massive misattributed a CIK to an
	// unrelated older entity — Brickell Biotech's BBI row carries
	// CIK 0000819050, which belongs to Arch Capital historically,
	// so SEC returned a 1994 filing that placed Brickell ahead of
	// Blockbuster (2010 delisting) in the sort and caused
	// reconcileBoundary to chop Brickell's correct 2022 delisting.
	// Ties keep the caller's original order via the index.
	sortIndexedByAssignedListing(indexed, assignments)

	// Cross-asset reconciliation between adjacent (predecessor,
	// successor) pairs. Walks the sorted slice and resolves boundary
	// disagreements by trusting the successor's Massive reference
	// listing date and forcing the predecessor's delisting to one
	// trading day before it.
	for i := 0; i+1 < len(indexed); i++ {
		reconcileBoundary(
			logger,
			&assignments[i], &assignments[i+1],
			indexed[i].candidates, indexed[i+1].candidates,
			previousTradingDay,
		)
	}

	// Final rule enforcement: every assignment must satisfy
	// delisting > listing, no overlap with neighbors, and the
	// active=false implies delisting-set rule. Violations are logged
	// at warn level and the offending field is cleared so we never
	// persist a known-bad value.
	enforceRules(logger, assignments, indexed)

	// Restore the caller's input order on the way out.
	out := make([]AssignedDates, len(candidates))
	for _, ic := range indexed {
		out[ic.index] = assignments[indexOf(indexed, ic.index)]
	}

	return out
}

// indexedCandidate is the internal sortable carrier used during the
// cross-asset assignment so the caller's original order can be
// restored on the way out.
type indexedCandidate struct {
	index      int
	candidates DateCandidates
}

func indexOf(indexed []indexedCandidate, originalIndex int) int {
	for i, ic := range indexed {
		if ic.index == originalIndex {
			return i
		}
	}

	return -1
}

// sortIndexedByAssignedListing reorders indexed and assignments in
// lockstep so that earlier chosen listing dates come first. A zero
// listing sorts last; ties preserve the existing relative order. The
// two slices must have the same length.
func sortIndexedByAssignedListing(indexed []indexedCandidate, assignments []AssignedDates) {
	type pair struct {
		ic indexedCandidate
		a  AssignedDates
	}

	pairs := make([]pair, len(indexed))
	for i := range indexed {
		pairs[i] = pair{ic: indexed[i], a: assignments[i]}
	}

	sort.SliceStable(pairs, func(i, j int) bool {
		li := pairs[i].a.ListingDate
		lj := pairs[j].a.ListingDate

		switch {
		case li.IsZero() && lj.IsZero():
			return pairs[i].ic.index < pairs[j].ic.index
		case li.IsZero():
			return false
		case lj.IsZero():
			return true
		}

		return li.Before(lj)
	})

	for i := range pairs {
		indexed[i] = pairs[i].ic
		assignments[i] = pairs[i].a
	}
}

// chooseDatesForAsset picks the listing and delisting dates for one
// asset from its own candidates, before any cross-asset reconciliation.
// Applies the priority order (Massive reference, EOD, walk, SEC) and
// the hard constraints (not in the future, listing < delisting). As a
// pre-filter, when Massive reference's own listing and delisting are
// not strictly ordered, both are dropped as a unit because either one
// of them is wrong and we cannot tell which.
func chooseDatesForAsset(c DateCandidates) AssignedDates {
	if !c.MassiveReferenceListingDate.IsZero() && !c.MassiveReferenceDelistingDate.IsZero() &&
		!c.MassiveReferenceDelistingDate.After(c.MassiveReferenceListingDate) {
		c.MassiveReferenceListingDate = time.Time{}
		c.MassiveReferenceDelistingDate = time.Time{}
	}

	listing, listingSource := chooseListingDate(c)
	delisting, delistingSource := chooseDelistingDate(c, listing)

	return AssignedDates{
		ListingDate:     listing,
		ListingSource:   listingSource,
		DelistingDate:   delisting,
		DelistingSource: delistingSource,
	}
}

// chooseListingDate returns the chosen listing date and its source
// name. Candidates are tried in priority order; the first that is
// usable and satisfies the constraints wins. The function must never
// return a zero time when at least one candidate (including the
// observation's ValidFor) is set: a null listed value in the database
// breaks downstream queries that treat null as "always active," so
// when every high-confidence candidate fails its gates the algorithm
// falls back to the earliest piece of evidence available and finally
// to ValidFor.
func chooseListingDate(c DateCandidates) (time.Time, string) {
	now := time.Now()
	upperBound := c.MassiveReferenceDelistingDate
	eodFirstBarBound := c.MassiveEODArchiveFirstBar

	// Reject Massive's list_date when a previous EOD lifecycle for
	// the same ticker ends after it (OSG-style ticker reassignment).
	massiveListingUsable := !c.MassiveReferenceListingDate.IsZero()
	if massiveListingUsable && !c.MassiveEODArchivePreviousLifecycleEnd.IsZero() &&
		c.MassiveReferenceListingDate.Before(c.MassiveEODArchivePreviousLifecycleEnd) {
		massiveListingUsable = false
	}

	// Reject Massive's list_date when EOD coverage was live during a
	// multi-year gap between it and the ticker's first bar (iShares
	// Morningstar rebrand, OTC-to-listed transitions).
	if massiveListingUsable && eodFirstBarUsableAsValue(c) {
		gap := c.MassiveEODArchiveFirstBar.Sub(c.MassiveReferenceListingDate)
		if gap > massiveListDateGapTolerance {
			massiveListingUsable = false
		}
	}

	tries := []struct {
		value  time.Time
		source string
		usable bool
	}{
		{
			c.MassiveReferenceListingDate,
			"massive_reference_listing_date",
			massiveListingUsable,
		},
		{
			c.MassiveEODArchiveFirstBar,
			"massive_eod_archive_first_bar",
			eodFirstBarUsableAsValue(c),
		},
		{
			c.MassiveReferenceWalkFirstSeen,
			"massive_reference_walk_first_seen",
			walkFirstSeenUsable(c),
		},
		{
			c.SECEarliestFilingMatchingForm,
			"sec_earliest_filing_matching_form",
			!c.SECEarliestFilingMatchingForm.IsZero(),
		},
		// Edge-bar fallbacks last. listed must never be null, so
		// when every higher-priority candidate is rejected (e.g.
		// SEC's earliest filing for a misattributed CIK postdates
		// the EOD first bar bound), use the earliest evidence we do
		// have of the asset trading. These edge-of-coverage values
		// cannot tell us whether trading started before our window,
		// but they at least say "the asset was trading by this
		// date" — which is the best we can honestly do.
		{
			c.MassiveEODArchiveFirstBar,
			"massive_eod_archive_first_bar_edge",
			!c.MassiveEODArchiveFirstBar.IsZero(),
		},
		{
			c.MassiveReferenceWalkFirstSeen,
			"massive_reference_walk_first_seen_edge",
			!c.MassiveReferenceWalkFirstSeen.IsZero(),
		},
		// Observation-date last-resort. When every other signal is
		// missing (no Massive reference list_date, no EOD archive
		// coverage, no walk window across any of the figi / CIK /
		// name indexes, no SEC submissions), the only fact we still
		// have is that the asset was alive on the date we observed
		// it. Using that date as the listing means we may overstate
		// it (the asset usually listed earlier), but the alternative
		// is a null listed value that breaks downstream queries
		// treating null as "always active". This fallback exists to
		// honor the listed-never-null invariant when nothing else
		// can.
		{
			c.ValidFor,
			"valid_for",
			!c.ValidFor.IsZero(),
		},
	}

	for _, t := range tries {
		if !t.usable {
			continue
		}

		if t.value.After(now) {
			continue
		}

		if !upperBound.IsZero() && !t.value.Before(upperBound) {
			continue
		}

		if !eodFirstBarBound.IsZero() && t.value.After(eodFirstBarBound) {
			continue
		}

		return t.value, t.source
	}

	return time.Time{}, ""
}

// chooseDelistingDate returns the chosen delisting date and its source
// name. Mirrors the listing logic: priority is Massive reference,
// then EOD last bar + 1 trading day, then walk last-seen + 1 trading
// day. SEC has no analogue. Each candidate must be strictly after the
// chosen listing date.
//
// For inactive assets (c.Active=false) the algorithm extends the chain
// with edge-bar fallbacks and a ValidFor last-resort, mirroring
// chooseListingDate's structure. The invariant the rest of the system
// relies on is: a row with active=false must carry a non-null delisted
// timestamp, so when every high-confidence candidate fails its gates
// we fall back to the latest piece of evidence available rather than
// leaving the field empty. Active assets, by contrast, are expected to
// have an open-ended (null) delisting and the fallback chain does not
// run for them.
func chooseDelistingDate(c DateCandidates, listing time.Time) (time.Time, string) {
	eodLastBarBound := plusOneCalendarDay(c.MassiveEODArchiveLastBar)

	tries := []struct {
		value  time.Time
		source string
		usable bool
	}{
		{
			c.MassiveReferenceDelistingDate,
			"massive_reference_delisting_date",
			!c.MassiveReferenceDelistingDate.IsZero(),
		},
		{
			plusOneCalendarDay(c.MassiveEODArchiveLastBar),
			"massive_eod_archive_last_bar",
			eodLastBarUsableAsValue(c),
		},
		{
			plusOneCalendarDay(c.MassiveReferenceWalkLastSeen),
			"massive_reference_walk_last_seen",
			walkLastSeenUsable(c),
		},
	}

	if !c.Active {
		// Edge-bar fallbacks for inactive assets. delisted must never
		// be null when active=false, so when every higher-priority
		// candidate is rejected (e.g. EOD last bar sits at coverage
		// end or walk last seen sits at the walk window edge) use
		// the latest evidence we do have of the asset trading. These
		// edge-of-coverage values cannot tell us whether trading
		// continued past our window, but they at least say "the
		// asset was last seen by this date" — which is the best we
		// can honestly do. No ValidFor / metadata last-resort: an
		// observation-date stamp is metadata mtime, not a trading
		// signal, so it cannot stand in for the date an asset
		// stopped trading. If neither EOD nor walk evidence exists
		// we genuinely do not know when the asset was delisted and
		// the algorithm returns zero so the caller can decide.
		tries = append(tries,
			struct {
				value  time.Time
				source string
				usable bool
			}{
				plusOneCalendarDay(c.MassiveEODArchiveLastBar),
				"massive_eod_archive_last_bar_edge",
				!c.MassiveEODArchiveLastBar.IsZero(),
			},
			struct {
				value  time.Time
				source string
				usable bool
			}{
				plusOneCalendarDay(c.MassiveReferenceWalkLastSeen),
				"massive_reference_walk_last_seen_edge",
				!c.MassiveReferenceWalkLastSeen.IsZero(),
			},
		)
	}

	for _, t := range tries {
		if !t.usable {
			continue
		}

		if !listing.IsZero() && !t.value.After(listing) {
			continue
		}

		if !eodLastBarBound.IsZero() && t.value.Before(eodLastBarBound) {
			continue
		}

		return t.value, t.source
	}

	return time.Time{}, ""
}

// eodFirstBarUsableAsValue reports whether the EOD first-bar candidate
// sits far enough past the archive's coverage start to be trusted as
// a real listing-date *value*. A bar at or near coverage start cannot
// be used as the listing date directly because we cannot tell whether
// trading began on that bar or before our coverage starts; in that
// case the bar is still used as an upper bound on the listing date
// (see chooseListingDate), just not as a candidate to return.
func eodFirstBarUsableAsValue(c DateCandidates) bool {
	if c.MassiveEODArchiveFirstBar.IsZero() || c.MassiveEODArchiveCoverageStart.IsZero() {
		return false
	}

	threshold := c.MassiveEODArchiveCoverageStart.AddDate(0, 0, edgeBufferDays)

	return c.MassiveEODArchiveFirstBar.After(threshold)
}

// eodLastBarUsableAsValue reports whether the EOD last-bar candidate
// sits far enough before the archive's coverage end to be trusted as
// a real delisting-date *value*. A bar at or near coverage end cannot
// be used as the delisting date directly because trading may still be
// continuing past our coverage; in that case the bar is still used
// as a lower bound on the delisting date (see chooseDelistingDate),
// just not as a candidate to return.
func eodLastBarUsableAsValue(c DateCandidates) bool {
	if c.MassiveEODArchiveLastBar.IsZero() || c.MassiveEODArchiveCoverageEnd.IsZero() {
		return false
	}

	threshold := c.MassiveEODArchiveCoverageEnd.AddDate(0, 0, -edgeBufferDays)

	return c.MassiveEODArchiveLastBar.Before(threshold)
}

// walkFirstSeenUsable reports whether the reference walk's FirstSeen
// candidate sits far enough inside the walk window to be trusted.
func walkFirstSeenUsable(c DateCandidates) bool {
	if c.MassiveReferenceWalkFirstSeen.IsZero() || c.MassiveReferenceWalkStart.IsZero() {
		return false
	}

	threshold := c.MassiveReferenceWalkStart.AddDate(0, 0, edgeBufferDays)

	return c.MassiveReferenceWalkFirstSeen.After(threshold)
}

// walkLastSeenUsable reports whether the reference walk's LastSeen
// candidate sits far enough before the walk window's end to be
// trusted as a real delisting signal.
func walkLastSeenUsable(c DateCandidates) bool {
	if c.MassiveReferenceWalkLastSeen.IsZero() || c.MassiveReferenceWalkEnd.IsZero() {
		return false
	}

	threshold := c.MassiveReferenceWalkEnd.AddDate(0, 0, -edgeBufferDays)

	return c.MassiveReferenceWalkLastSeen.Before(threshold)
}

// plusOneCalendarDay returns t + 1 calendar day, preserving a zero
// time.Time. Used to convert a last-trade observation into a
// delisting-day candidate. Trading-day-aware adjustment happens later
// in the cross-asset reconciliation step via previousTradingDayFn.
func plusOneCalendarDay(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}

	return t.AddDate(0, 0, 1)
}

// boundaryDisagreementDays is how far apart Massive's reference
// listing date for the successor and the observed EOD activity may
// be before we treat the disagreement as evidence of an upstream data
// error rather than ordinary registration-vs-trade-date noise.
const boundaryDisagreementDays = 30

// rejectOverlappingMassiveListings keeps Massive's list_date on the
// earliest lifecycle for the ticker and clears it on every later
// lifecycle that shares the same value. Later lifecycles re-pick from
// the next priority candidate (typically their own EOD first bar).
func rejectOverlappingMassiveListings(logger *zerolog.Logger, indexed []indexedCandidate, assignments []AssignedDates) {
	if len(indexed) < 2 {
		return
	}

	// Group by Massive listing date.
	byDate := make(map[time.Time][]int, len(indexed))

	for i, ic := range indexed {
		d := ic.candidates.MassiveReferenceListingDate
		if d.IsZero() {
			continue
		}

		byDate[d] = append(byDate[d], i)
	}

	for d, group := range byDate {
		if len(group) < 2 {
			continue
		}

		// Pick the earliest lifecycle by EOD first bar; that one
		// keeps Massive's listing.
		keep := group[0]
		for _, i := range group[1:] {
			if lifecycleStartBefore(indexed[i].candidates, indexed[keep].candidates) {
				keep = i
			}
		}

		for _, i := range group {
			if i == keep {
				continue
			}

			logger.Info().
				Time("RejectedMassiveListingDate", d).
				Int("AssetIndex", indexed[i].index).
				Int("KeptOnAssetIndex", indexed[keep].index).
				Msg("massive: clearing Massive listing on later-lifecycle sibling sharing the predecessor's value")

			updated := indexed[i].candidates
			updated.MassiveReferenceListingDate = time.Time{}
			indexed[i].candidates = updated
			assignments[i] = chooseDatesForAsset(updated)
		}
	}
}

// lifecycleStartBefore reports whether a's lifecycle starts strictly
// before b's. Prefers EOD first bar; falls back to walk first-seen
// when EOD is missing (e.g., archive not mounted).
func lifecycleStartBefore(a, b DateCandidates) bool {
	aStart := lifecycleStart(a)
	bStart := lifecycleStart(b)

	if aStart.IsZero() {
		return !bStart.IsZero()
	}

	if bStart.IsZero() {
		return false
	}

	return aStart.Before(bStart)
}

// lifecycleStart returns the earliest known start date for an asset's
// lifecycle: EOD first bar if present, otherwise walk first-seen.
func lifecycleStart(c DateCandidates) time.Time {
	if !c.MassiveEODArchiveFirstBar.IsZero() {
		return c.MassiveEODArchiveFirstBar
	}

	return c.MassiveReferenceWalkFirstSeen
}

// reconcileBoundary resolves the (predecessor, successor) boundary
// between two adjacent assets that share a ticker. The successor's
// Massive reference listing date is the authoritative boundary when
// available; the predecessor's delisting is forced to one trading day
// before it so the two windows are adjacent and non-overlapping.
func reconcileBoundary(
	logger *zerolog.Logger,
	predecessor, successor *AssignedDates,
	predecessorCandidates, successorCandidates DateCandidates,
	previousTradingDay previousTradingDayFn,
) {
	// Apply the same previous-lifecycle gate that chooseListingDate
	// uses: a successor's MassiveReferenceListingDate that predates
	// the previous EOD lifecycle's end is from a different (earlier)
	// context on the same ticker and is not the successor's actual
	// listing. Fall back to the successor's already-chosen ListingDate
	// in that case so reconcileBoundary doesn't reinstate a value the
	// per-asset choice already rejected. Track whether the boundary
	// came from Massive's reference so the source label written back
	// onto the successor only claims "massive_reference_listing_date"
	// when that is actually true.
	boundary := successorCandidates.MassiveReferenceListingDate
	boundaryFromMassiveRef := !boundary.IsZero()

	if boundaryFromMassiveRef &&
		!successorCandidates.MassiveEODArchivePreviousLifecycleEnd.IsZero() &&
		boundary.Before(successorCandidates.MassiveEODArchivePreviousLifecycleEnd) {
		boundary = time.Time{}
		boundaryFromMassiveRef = false
	}

	if boundary.IsZero() {
		boundary = successor.ListingDate
	}

	if boundary.IsZero() {
		return
	}

	// Only act when the predecessor's currently-assigned delisting
	// would conflict with the chosen boundary. Two same-ticker assets
	// whose lifecycles are separated by years (e.g. BBI's Blockbuster
	// delisting in 2010 and Brickell listing in 2019) have no
	// boundary disagreement to resolve, and pulling the predecessor's
	// delisting forward to the day before the successor's listing
	// would erase the correct EOD-derived value.
	if !predecessor.DelistingDate.IsZero() && predecessor.DelistingDate.Before(boundary) {
		return
	}

	// Cross-check the boundary against EOD evidence for the
	// successor. If EOD says the successor's bars do not start until
	// significantly after Massive's claimed listing date, log it.
	if successorCandidates.eodFirstBarUsableForCrossCheck() {
		gap := successorCandidates.MassiveEODArchiveFirstBar.Sub(boundary)
		if gap > time.Duration(boundaryDisagreementDays)*24*time.Hour {
			logger.Warn().
				Time("MassiveReferenceListingDate", boundary).
				Time("EODFirstBar", successorCandidates.MassiveEODArchiveFirstBar).
				Dur("Gap", gap).
				Msg("massive: successor listing date and EOD first bar disagree; possible Massive reference error")
		}
	}

	prev := previousTradingDay(boundary)
	if prev.IsZero() {
		return
	}

	predecessor.DelistingDate = prev
	predecessor.DelistingSource = "successor_boundary_minus_one_trading_day"
	successor.ListingDate = boundary

	if boundaryFromMassiveRef {
		successor.ListingSource = "massive_reference_listing_date"
	}
}

// eodFirstBarUsableForCrossCheck is the same usability rule as
// eodFirstBarUsableAsValue but exposed as a method so reconcileBoundary
// can keep its call site readable.
func (c DateCandidates) eodFirstBarUsableForCrossCheck() bool {
	return eodFirstBarUsableAsValue(c)
}

// enforceRules walks the final assignments and confirms each one
// satisfies the three immutable rules. Violations are logged at warn
// level and the offending field is cleared so callers never persist a
// value the algorithm could not validate.
func enforceRules(logger *zerolog.Logger, assignments []AssignedDates, indexed []indexedCandidate) {
	for i := range assignments {
		a := &assignments[i]
		c := indexed[i].candidates

		// Rule 2: delisting strictly after listing.
		if !a.ListingDate.IsZero() && !a.DelistingDate.IsZero() && !a.DelistingDate.After(a.ListingDate) {
			logger.Warn().
				Time("Listing", a.ListingDate).
				Time("Delisting", a.DelistingDate).
				Msg("massive: rule violation: delisting is not after listing; clearing delisting")

			a.DelistingDate = time.Time{}
			a.DelistingSource = ""
		}

		// Rule 3: active=false implies delisting must be set.
		if !c.Active && a.DelistingDate.IsZero() {
			logger.Warn().
				Bool("Active", c.Active).
				Time("Listing", a.ListingDate).
				Msg("massive: rule violation: inactive asset has no delisting date; no candidate could supply one")
		}
	}

	// Rule 1: no overlapping windows among assets sharing the ticker.
	// indexed[] is sorted by earliest-known listing, so checking each
	// pair against every later pair catches every violation.
	//
	// A zero (NULL) endpoint is treated as the open boundary in the
	// direction it would extend: a zero listing means the window
	// extends backwards from the delisting (or all the way back if
	// delisting is also zero); a zero delisting means the window
	// extends forwards from the listing (or all the way forwards if
	// listing is also zero). Two windows overlap when, after this
	// open-boundary expansion, they share any point in time. A row
	// whose listing and delisting are both zero overlaps every
	// sibling by definition — we have no information to place it.
	for i := 0; i+1 < len(indexed); i++ {
		earlier := &assignments[i]

		for j := i + 1; j < len(indexed); j++ {
			later := &assignments[j]

			if !windowsOverlap(earlier, later) {
				continue
			}

			logger.Warn().
				Str("EarlierField", fmt.Sprintf("asset_index_%d", indexed[i].index)).
				Time("EarlierListing", earlier.ListingDate).
				Time("EarlierDelisting", earlier.DelistingDate).
				Str("LaterField", fmt.Sprintf("asset_index_%d", indexed[j].index)).
				Time("LaterListing", later.ListingDate).
				Time("LaterDelisting", later.DelistingDate).
				Msg("massive: rule violation: same-ticker asset windows overlap")
		}
	}
}

// windowsOverlap reports whether two assignments' (listing, delisting]
// windows share any point in time, treating a zero endpoint as the
// open boundary in the direction it would extend. See enforceRules'
// Rule 1 comment for the precise semantics.
func windowsOverlap(a, b *AssignedDates) bool {
	aStart, aEnd := windowBounds(a)
	bStart, bEnd := windowBounds(b)

	if aEnd.Before(bStart) {
		return false
	}

	if bEnd.Before(aStart) {
		return false
	}

	return true
}

// windowBounds returns (start, end) for an assignment with the
// open-boundary expansion described above. A zero listing maps to the
// earliest representable time; a zero delisting maps to the latest.
func windowBounds(a *AssignedDates) (time.Time, time.Time) {
	start := a.ListingDate
	if start.IsZero() {
		start = time.Time{}.Add(time.Nanosecond)
	}

	end := a.DelistingDate
	if end.IsZero() {
		end = time.Unix(1<<62, 0)
	}

	return start, end
}
