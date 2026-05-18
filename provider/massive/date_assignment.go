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

// DateCandidates collects the per-asset inputs the date-assignment
// algorithm consumes when choosing a listing date and a delisting date
// for one asset that belongs to a ticker. The struct is self-contained:
// every value the algorithm needs is here, so the algorithm itself is
// a pure function over a slice of these.
//
// All date fields are time.Time; an unset value is the zero time.Time
// (use IsZero() to test). Source strings on the output match the field
// names below in snake_case so the JSONB lineage column records the
// source unambiguously.
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
	MassiveEODArchiveFirstBar      time.Time
	MassiveEODArchiveLastBar       time.Time
	MassiveEODArchiveCoverageStart time.Time
	MassiveEODArchiveCoverageEnd   time.Time

	// SEC EDGAR signals. FormPrefix is the registration form used for
	// this asset type ("N-1A" for ETF / MutualFund, "N-2" for CEF, ""
	// for non-fund types where we use earliest filing of any kind).
	// EarliestFilingMatchingForm is the resulting date.
	SECFormPrefix                 string
	SECEarliestFilingMatchingForm time.Time
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
//
// The function enforces three immutable rules across the result:
//
//  1. No two assets sharing the ticker have overlapping (listing,
//     delisting] windows.
//  2. Each asset's delisting date is strictly after its listing date.
//  3. Any asset whose Active flag is false must have a non-zero
//     delisting date.
//
// Disagreements between source signals are resolved per the
// per-asset priority order (Massive reference, EOD, walk, SEC) with
// cross-checks between the first three. When the candidates point at
// a likely data error the function logs at warn level so an operator
// can audit; it does not silently invent a date in those cases.
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

	// Sort by earliest-known-listing-date so predecessor precedes
	// successor. Ties keep the caller's original order via the index.
	sort.SliceStable(indexed, func(i, j int) bool {
		ei := earliestKnownListing(indexed[i].candidates)
		ej := earliestKnownListing(indexed[j].candidates)

		switch {
		case ei.IsZero() && ej.IsZero():
			return indexed[i].index < indexed[j].index
		case ei.IsZero():
			return false
		case ej.IsZero():
			return true
		}

		return ei.Before(ej)
	})

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

// earliestKnownListing returns the earliest non-zero listing-side
// candidate value for an asset across all sources. Used as the sort
// key when ordering predecessor before successor.
func earliestKnownListing(c DateCandidates) time.Time {
	candidates := []time.Time{
		c.MassiveReferenceListingDate,
		c.MassiveReferenceWalkFirstSeen,
		c.MassiveEODArchiveFirstBar,
		c.SECEarliestFilingMatchingForm,
	}

	var earliest time.Time

	for _, t := range candidates {
		if t.IsZero() {
			continue
		}

		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}

	return earliest
}

// chooseDatesForAsset picks the listing and delisting dates for one
// asset from its own candidates, before any cross-asset reconciliation.
// Applies the priority order (Massive reference, EOD, walk, SEC) and
// the hard constraints (not in the future, listing < delisting).
//
// Pre-filter: when Massive reference's own listing and delisting are
// not strictly ordered, both are dropped as a unit. Either one of them
// is wrong and we cannot tell which; the algorithm falls back to the
// other sources rather than persist a known-bad value.
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
// usable and satisfies the constraints wins.
//
// EOD evidence acts in two ways. When the first bar sits comfortably
// inside our archive coverage it is a candidate value (the asset
// likely started trading at that bar). When the first bar sits at
// the start of our coverage it is only a bound: we know the asset
// was already trading by that date but cannot tell whether trading
// started earlier. Either way, any candidate that would place the
// listing date strictly after the first bar contradicts the bound
// and is rejected.
//
// The function must never return a zero time: a null listed value in
// the database breaks downstream queries that treat null as "always
// active." When every high-confidence candidate fails its gates, the
// algorithm falls back to the earliest piece of evidence we do have
// of the asset trading — the EOD first bar at the coverage edge, or
// the walk first-seen at the walk-start edge — even though those
// values are imprecise. A best-guess listing date is always
// preferable to no listing date at all.
//
// The relationship between the chosen listing and the chosen
// delisting is enforced at the delisting step (chooseDelistingDate
// rejects any candidate that is not strictly after the chosen
// listing).
func chooseListingDate(c DateCandidates) (time.Time, string) {
	now := time.Now()
	upperBound := c.MassiveReferenceDelistingDate
	eodFirstBarBound := c.MassiveEODArchiveFirstBar

	tries := []struct {
		value  time.Time
		source string
		usable bool
	}{
		{
			c.MassiveReferenceListingDate,
			"massive_reference_listing_date",
			!c.MassiveReferenceListingDate.IsZero(),
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
// EOD evidence acts the same way for delisting that it does for
// listing, in mirror image. When the last bar sits comfortably
// inside coverage it is a candidate value. When it sits at the end
// of our coverage it is only a lower bound: the asset traded at
// least until that date and may still be trading. Either way, any
// candidate that would place the delisting strictly before the last
// bar contradicts the bound and is rejected.
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

// rejectOverlappingMassiveListings clears MassiveReferenceListingDate
// on every asset in a group whose Massive listing date is also
// claimed by at least one sibling, then re-picks that asset's dates
// from the next priority candidate. The signal — two same-ticker
// assets carrying identical Massive listing dates — is unambiguous
// evidence that Massive returned the ticker's first-allocation date
// as a per-asset value (e.g. BBI returns 1993-03-16 for both
// Blockbuster's 2003-2010 lifecycle and Brickell's 2019-2022
// lifecycle). The fix lets each asset fall through to EOD's first
// bar, the walk's first seen, or SEC's earliest filing, whichever
// the priority chain reaches first.
//
// The asymmetric case (siblings with different Massive listing
// dates whose windows still overlap once chosen) is intentionally
// not handled here. That case is the Alcoa-style boundary problem
// where the later asset's Massive listing is correct and the earlier
// asset's delisting just needs to retreat; reconcileBoundary handles
// it correctly downstream. Suppressing the later asset's Massive
// listing in that scenario would discard a value that is actually
// right.
func rejectOverlappingMassiveListings(logger *zerolog.Logger, indexed []indexedCandidate, assignments []AssignedDates) {
	if len(indexed) < 2 {
		return
	}

	massiveListings := make(map[time.Time]int, len(indexed))

	for _, ic := range indexed {
		d := ic.candidates.MassiveReferenceListingDate
		if d.IsZero() {
			continue
		}

		massiveListings[d]++
	}

	for i := range indexed {
		d := indexed[i].candidates.MassiveReferenceListingDate
		if d.IsZero() {
			continue
		}

		if massiveListings[d] < 2 {
			continue
		}

		logger.Info().
			Time("RejectedMassiveListingDate", d).
			Int("AssetIndex", indexed[i].index).
			Int("SiblingsWithSameDate", massiveListings[d]).
			Msg("massive: rejecting Massive reference listing date because the same value is claimed for multiple same-ticker assets; falling through to next priority candidate")

		updated := indexed[i].candidates
		updated.MassiveReferenceListingDate = time.Time{}
		indexed[i].candidates = updated
		assignments[i] = chooseDatesForAsset(updated)
	}
}

// reconcileBoundary resolves the (predecessor, successor) boundary
// between two adjacent assets that share a ticker. The successor's
// Massive reference listing date is the authoritative boundary when
// available; the predecessor's delisting is forced to one trading day
// before it so the two windows are adjacent and non-overlapping.
//
// When Massive's reference listing for the successor disagrees with
// the EOD evidence by more than boundaryDisagreementDays, the function
// logs a warning so an operator can investigate; the data-error path
// described in the design (look up the right boundary from SEC and
// the reference walk) is not yet automated.
func reconcileBoundary(
	logger *zerolog.Logger,
	predecessor, successor *AssignedDates,
	predecessorCandidates, successorCandidates DateCandidates,
	previousTradingDay previousTradingDayFn,
) {
	boundary := successorCandidates.MassiveReferenceListingDate
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
	successor.ListingSource = "massive_reference_listing_date"
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
	// indexed[] is sorted by earliest-known listing, so checking
	// adjacent pairs is sufficient.
	for i := 0; i+1 < len(indexed); i++ {
		later := &assignments[i+1]
		earlier := &assignments[i]

		if earlier.DelistingDate.IsZero() || later.ListingDate.IsZero() {
			continue
		}

		if !earlier.DelistingDate.Before(later.ListingDate) {
			logger.Warn().
				Str("EarlierField", fmt.Sprintf("asset_index_%d", indexed[i].index)).
				Time("EarlierDelisting", earlier.DelistingDate).
				Str("LaterField", fmt.Sprintf("asset_index_%d", indexed[i+1].index)).
				Time("LaterListing", later.ListingDate).
				Msg("massive: rule violation: same-ticker asset windows overlap; clearing earlier delisting")

			earlier.DelistingDate = time.Time{}
			earlier.DelistingSource = ""
		}
	}
}
