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

package sec

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
)

const (
	companyFactsURL    = "https://data.sec.gov/api/xbrl/companyfacts/"
	companyFactsZipURL = "https://www.sec.gov/Archives/edgar/daily-index/xbrl/companyfacts.zip"
	companyTickersURL  = "https://www.sec.gov/files/company_tickers.json"
	// edgarFeedPageSize is the number of entries returned per EDGAR feed page.
	edgarFeedPageSize = 100
	// edgarFeedURLFormat is a format string for the EDGAR ATOM feed; the %d
	// placeholder is the start offset used for pagination.
	edgarFeedURLFormat = "https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=10-K%%2C10-Q&dateb=&owner=include&count=100&search_text=&start=%d&output=atom"
)

// Fact represents a single XBRL fact value from an SEC filing.
type Fact struct {
	End   time.Time // Period end date (always present)
	Start time.Time // Period start date (present for duration concepts; zero for instant concepts)
	Filed time.Time // Date the filing was submitted to SEC
	Val   float64   // The reported value
	Accn  string    // SEC accession number (parsed but not currently used)
	Form  string    // Filing form type (10-K, 10-Q)
	FP    string    // Fiscal period (FY, Q1, Q2, Q3, Q4)
	Frame string    // XBRL frame identifier (e.g. CY2023Q3I)
	FY    int       // Fiscal year
}

// ClassSharesFact captures a Class A or Class B raw cover-page share count
// for the market-ratio share_factor formula. Only raw CommonClassAMember /
// CommonClassBMember contexts are captured; equivalent (B-unit) totals are
// tracked elsewhere.
type ClassSharesFact struct {
	Filed   time.Time // Date the filing was submitted to SEC
	End     time.Time // Cover-page instant date
	Form    string    // 10-K or 10-Q
	Concept string    // Source concept name (EntityCommonStockSharesOutstanding preferred over CommonStockSharesOutstanding)
	Class   string    // "A" or "B"
	Val     float64   // Raw share count (Class A: small; Class B: large)
}

// CompanyFacts holds parsed SEC EDGAR companyfacts data for a single entity.
type CompanyFacts struct {
	CIK        int               // Central Index Key
	EntityName string            // Company name
	Facts      map[string][]Fact // Map of concept name to facts (e.g. "Assets" -> []Fact)

	// NonPreferredUnitConcepts tracks concepts loaded from non-USD units;
	// these values may be foreign-currency denominations that dimensional
	// synthesis can replace.
	NonPreferredUnitConcepts map[string]bool

	// ClassShares collects Class A and Class B cover-page share counts from
	// dual-class filings; consumed by resolveClassSharesAsOf.
	ClassShares []ClassSharesFact
}

// FilterByFilingDate drops facts filed after cutoff so the view reproduces
// the data available at a historical point in time.
func (cf *CompanyFacts) FilterByFilingDate(cutoff time.Time) {
	for concept, facts := range cf.Facts {
		kept := facts[:0]

		for i := range facts {
			if !facts[i].Filed.After(cutoff) {
				kept = append(kept, facts[i])
			}
		}

		if len(kept) == 0 {
			delete(cf.Facts, concept)
		} else {
			cf.Facts[concept] = kept
		}
	}
}

// unitPreference defines the priority order for selecting unit types.
// Lower index = higher preference.
var unitPreference = []string{"USD", "USD/shares", "shares", "pure"}

const dateFormat = "2006-01-02"

// ParseCompanyFacts parses SEC EDGAR companyfacts JSON. Only 10-K/10-Q facts
// are included; the preferred unit type is selected per concept; all
// namespaces (us-gaap, dei, company extensions) are parsed.
func ParseCompanyFacts(jsonData []byte) (*CompanyFacts, error) {
	if !gjson.ValidBytes(jsonData) {
		return nil, fmt.Errorf("invalid JSON data")
	}

	root := gjson.ParseBytes(jsonData)

	cf := &CompanyFacts{
		CIK:        int(root.Get("cik").Int()),
		EntityName: root.Get("entityName").String(),
		Facts:      make(map[string][]Fact),
	}

	// Parse all namespaces, not just us-gaap/dei: company extension
	// namespaces contain custom concepts for non-standard line items.
	root.Get("facts").ForEach(func(nsName, nsData gjson.Result) bool {
		parseNamespaceFacts(nsData, cf)
		return true
	})

	normalizeShareScale(cf)

	return cf, nil
}

// parseNamespaceFacts extracts XBRL facts from a single taxonomy namespace
// (e.g. us-gaap or dei) and merges them into cf.Facts.
func parseNamespaceFacts(nsData gjson.Result, cf *CompanyFacts) {
	nsData.ForEach(func(conceptName, conceptData gjson.Result) bool {
		units := conceptData.Get("units")
		if !units.Exists() {
			return true
		}

		// Select the preferred unit type
		var selectedUnit gjson.Result

		for _, unitName := range unitPreference {
			candidate := units.Get(unitName)
			if candidate.Exists() {
				selectedUnit = candidate

				break
			}
		}

		// Fall back to the first available unit. Foreign-currency-only
		// concepts get loaded as placeholders for dimensional synthesis
		// to replace with consolidated values later.
		isNonPreferred := false

		if !selectedUnit.Exists() {
			isNonPreferred = true

			units.ForEach(func(_, unitData gjson.Result) bool {
				selectedUnit = unitData
				return false // stop after first
			})
		}

		if !selectedUnit.Exists() {
			return true
		}

		var facts []Fact

		selectedUnit.ForEach(func(_, entry gjson.Result) bool {
			form := entry.Get("form").String()

			// Only include 10-K and 10-Q filings
			if form != "10-K" && form != "10-Q" {
				return true
			}

			f := Fact{
				Val:   entry.Get("val").Float(),
				Accn:  entry.Get("accn").String(),
				Form:  form,
				FP:    entry.Get("fp").String(),
				Frame: entry.Get("frame").String(),
				FY:    int(entry.Get("fy").Int()),
			}

			// Parse end date
			if endStr := entry.Get("end").String(); endStr != "" {
				if t, err := time.Parse(dateFormat, endStr); err == nil {
					f.End = t
				}
			}

			// Parse start date (only present for duration concepts)
			if startStr := entry.Get("start").String(); startStr != "" {
				if t, err := time.Parse(dateFormat, startStr); err == nil {
					f.Start = t
				}
			}

			// Parse filed date
			if filedStr := entry.Get("filed").String(); filedStr != "" {
				if t, err := time.Parse(dateFormat, filedStr); err == nil {
					f.Filed = t
				}
			}

			facts = append(facts, f)

			return true
		})

		if len(facts) > 0 {
			// Sort facts by Filed ascending so ResolveFieldsForFiling can use
			// binary search to slice the prefix of facts available at any
			// given filing date. Stable sort preserves JSON parse order for
			// same-day filings so downstream resolution is deterministic.
			sort.SliceStable(facts, func(i, j int) bool {
				return facts[i].Filed.Before(facts[j].Filed)
			})

			cf.Facts[conceptName.String()] = facts

			if isNonPreferred {
				if cf.NonPreferredUnitConcepts == nil {
					cf.NonPreferredUnitConcepts = make(map[string]bool)
				}

				cf.NonPreferredUnitConcepts[conceptName.String()] = true
			}
		}

		return true
	})
}

// weightedAvgShareConcepts are share concepts that some filers report in
// compact units (millions/thousands) rather than absolute counts;
// normalizeShareScale rescales them.
var weightedAvgShareConcepts = []string{
	"WeightedAverageNumberOfSharesOutstandingBasic",
	"WeightedAverageNumberOfDilutedSharesOutstanding",
	"WeightedAverageNumberOfShareOutstandingBasicAndDiluted",
	"WeightedAverageNumberOfSharesIssuedBasic",
}

// normalizeShareScale rescales weighted-average share facts filed in compact
// units to absolute counts, detecting per-fact against the temporally-nearest
// EntityCommonStockSharesOutstanding reference.
func normalizeShareScale(cf *CompanyFacts) {
	refFacts := cf.Facts["EntityCommonStockSharesOutstanding"]
	if len(refFacts) == 0 {
		return
	}

	for _, concept := range weightedAvgShareConcepts {
		facts, ok := cf.Facts[concept]
		if !ok {
			continue
		}

		for i := range facts {
			v := math.Abs(facts[i].Val)
			if v == 0 {
				continue
			}

			ref := nearestRefShareCount(refFacts, facts[i].End)
			// Require a large enough reference for the ratio to be meaningful.
			if ref < 1_000_000 {
				continue
			}

			ratio := ref / v
			switch {
			case ratio >= 3e5 && ratio <= 3e6:
				facts[i].Val *= 1e6
			case ratio >= 3e2 && ratio <= 3e3:
				facts[i].Val *= 1e3
			}
		}
	}
}

// nearestRefShareCount returns the absolute value of the reference fact
// whose End date is closest to asOfDate, or 0 if no candidate exists.
func nearestRefShareCount(refFacts []Fact, asOfDate time.Time) float64 {
	var (
		best     *Fact
		bestDiff time.Duration
	)

	for i := range refFacts {
		if refFacts[i].End.IsZero() {
			continue
		}

		diff := refFacts[i].End.Sub(asOfDate)
		if diff < 0 {
			diff = -diff
		}

		if best == nil || diff < bestDiff {
			best = &refFacts[i]
			bestDiff = diff
		}
	}

	if best == nil {
		return 0
	}

	return math.Abs(best.Val)
}

// FetchCompanyFacts downloads the companyfacts JSON for a single CIK from SEC EDGAR.
func FetchCompanyFacts(ctx context.Context, client *resty.Client, cik int) (*CompanyFacts, error) {
	url := companyFactsURL + FormatCIK(cik) + ".json"

	resp, err := client.R().
		SetContext(ctx).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching companyfacts for CIK %d: %w", cik, err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("SEC returned status %d for CIK %d", resp.StatusCode(), cik)
	}

	return ParseCompanyFacts(resp.Body())
}

// submissionsURL is the SEC EDGAR endpoint for company submission metadata.
const submissionsURL = "https://data.sec.gov/submissions/"

// EnrichWithExtensionFacts fetches recent 10-K/10-Q inline XBRL filings and
// parses extension facts (concepts outside us-gaap/dei) into cf.Facts.
func EnrichWithExtensionFacts(ctx context.Context, client *resty.Client, cik int, cf *CompanyFacts) {
	// Fetch the submissions metadata to find recent filings.
	subURL := submissionsURL + FormatCIK(cik) + ".json"

	resp, err := client.R().
		SetContext(ctx).
		Get(subURL)
	if err != nil || resp.StatusCode() != http.StatusOK {
		log.Debug().Err(err).Int("cik", cik).Msg("failed to fetch submissions for extension enrichment")
		return
	}

	root := gjson.ParseBytes(resp.Body())

	// Collect filings until we've seen the third 10-K so Q4 synthesis for the
	// prior fiscal year has consistent extension facts across two fiscal
	// years of quarterly filings.
	type filingInfo struct {
		accession  string
		doc        string
		filedDate  string
		reportDate string
		form       string
	}

	var filings []filingInfo

	kCount := 0

	// If a filing cutoff is set, skip filings filed after it.
	cutoff, hasCutoff := provider.FilingCutoffFromContext(ctx)

	// scanFilingList scans a gjson filing list (recent or overflow) for
	// 10-K and 10-Q filings. Returns true when we've found enough.
	scanFilingList := func(list gjson.Result) bool {
		forms := list.Get("form").Array()
		accessions := list.Get("accessionNumber").Array()
		docs := list.Get("primaryDocument").Array()
		filingDates := list.Get("filingDate").Array()
		reportDates := list.Get("reportDate").Array()

		for i := range forms {
			form := forms[i].String()
			if form != "10-K" && form != "10-Q" {
				continue
			}

			fi := filingInfo{
				accession: accessions[i].String(),
				doc:       docs[i].String(),
				filedDate: filingDates[i].String(),
				form:      form,
			}

			if i < len(reportDates) {
				fi.reportDate = reportDates[i].String()
			}

			if hasCutoff {
				if filedTime, err := time.Parse(dateFormat, fi.filedDate); err == nil && filedTime.After(cutoff) {
					continue
				}
			}

			filings = append(filings, fi)

			if form == "10-K" {
				kCount++
				if kCount >= 3 {
					return true
				}
			}
		}

		return false
	}

	// Scan the recent filings first.
	if scanFilingList(root.Get("filings.recent")) {
		goto done
	}

	// Large filers (e.g. JPM) push older 10-K/10-Q filings into overflow
	// files; scan those if recent didn't have enough.
	{
		overflowFiles := root.Get("filings.files").Array()
		for _, f := range overflowFiles {
			if ctx.Err() != nil {
				break
			}

			fileName := f.Get("name").String()
			if fileName == "" {
				continue
			}

			overflowURL := submissionsURL + fileName

			overflowResp, err := client.R().SetContext(ctx).Get(overflowURL)
			if err != nil || overflowResp.StatusCode() != http.StatusOK {
				log.Debug().Str("file", fileName).Msg("failed to fetch overflow submissions file")
				continue
			}

			if scanFilingList(gjson.ParseBytes(overflowResp.Body())) {
				break
			}
		}
	}

done:

	// Process oldest first so the original 10-Q's dimensional synthesis facts
	// are established before later filings' comparatives, otherwise the
	// restated comparative would block the original synthesis.
	for i, j := 0, len(filings)-1; i < j; i, j = i+1, j-1 {
		filings[i], filings[j] = filings[j], filings[i]
	}

	log.Debug().Int("cik", cik).Int("filings", len(filings)).Msg("enriching with extension facts from XBRL instance documents")

	for _, fi := range filings {
		if ctx.Err() != nil {
			return
		}

		parseExtensionFactsFromFiling(ctx, client, cik, cf, fi.accession, fi.doc, fi.filedDate, fi.reportDate, fi.form)
	}

	// Log extension facts found for key concepts.
	for _, key := range []string{
		"DepreciationAmortizationAndOther",
		"GoodwillServicingAssetsAtFairValueAndOtherIntangibleAssets",
		"AccruedInterestAndAccountsReceivable",
		"PropertyPlantAndEquipmentAndOperatingLeaseRightOfUseAssetAfterAccumulatedDepreciationAndAmortization",
	} {
		if facts, ok := cf.Facts[key]; ok {
			for _, f := range facts {
				log.Debug().Str("concept", key).Time("end", f.End).Time("start", f.Start).Time("filed", f.Filed).Str("form", f.Form).Float64("val", f.Val).Msg("extension fact")
			}
		}
	}

	log.Debug().Int("cik", cik).Int("total_concepts", len(cf.Facts)).Msg("extension enrichment complete")
}

// parseExtensionFactsFromFiling downloads the extracted XBRL instance XML
// for a filing and parses extension facts using encoding/xml.
func parseExtensionFactsFromFiling(ctx context.Context, client *resty.Client, cik int, cf *CompanyFacts,
	accession, primaryDoc, filedDate, reportDate, formType string) {
	accessionPath := strings.ReplaceAll(accession, "-", "")

	// The extracted XBRL instance XML file uses the _htm.xml suffix.
	xmlDoc := strings.TrimSuffix(primaryDoc, ".htm") + "_htm.xml"
	docURL := fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%d/%s/%s",
		cik, accessionPath, xmlDoc)

	resp, err := client.R().
		SetContext(ctx).
		Get(docURL)
	if err != nil || resp.StatusCode() != http.StatusOK {
		return
	}

	filed, _ := time.Parse("2006-01-02", filedDate)

	parseXBRLInstanceExtensions(resp.Body(), cf, filed, formType)
}

// dimensionalShareConcepts lists us-gaap per-share/share-count concepts that
// multi-class filers report only with dimensional XBRL. The companyfacts API
// excludes dimensional facts, so we capture them from inline XBRL.
var dimensionalShareConcepts = map[string]bool{
	"WeightedAverageNumberOfSharesOutstandingBasic":          true,
	"WeightedAverageNumberOfDilutedSharesOutstanding":        true,
	"WeightedAverageNumberOfShareOutstandingBasicAndDiluted": true,
	"EarningsPerShareBasic":                                  true,
	"EarningsPerShareDiluted":                                true,
	"CommonStockSharesOutstanding":                           true,
	"EntityCommonStockSharesOutstanding":                     true, // DEI cover-page shares; multi-class filers report per-class
}

// contextHasClassBMember reports whether any dimension member contains
// "ClassB" (matching us-gaap:CommonClassBMember and company extensions).
func contextHasClassBMember(ctx contextPeriod) bool {
	for _, m := range ctx.dimMembers {
		if strings.Contains(m, "ClassB") {
			return true
		}
	}

	return false
}

// contextHasClassAMember reports whether any dimension member contains
// "ClassA". Used with contextHasClassBMember for share-count concepts where
// both class totals must be summed.
func contextHasClassAMember(ctx contextPeriod) bool {
	for _, m := range ctx.dimMembers {
		if strings.Contains(m, "ClassA") {
			return true
		}
	}

	return false
}

// multiClassShareCountConcepts lists share-count concepts whose per-class
// values are raw counts to sum across classes. EPS/WAS concepts are NOT in
// this set: Sharadar reports those in Class B equivalent units, so only the
// Class B dimensional value is captured.
var multiClassShareCountConcepts = map[string]bool{
	"CommonStockSharesOutstanding":       true,
	"EntityCommonStockSharesOutstanding": true,
}

// rawFact holds a parsed XBRL fact from the inline XBRL instance document.
type rawFact struct {
	conceptName    string
	contextRef     string
	unitRef        string
	value          float64
	isShareConcept bool // true for us-gaap share/EPS concepts captured for dimensional resolution
	isGapFill      bool // true for standard us-gaap concepts captured to fill companyfacts API gaps
}

// hasFactForPeriod reports whether cf already contains a fact for concept
// and period end. Used to avoid duplicating data from the companyfacts API.
func hasFactForPeriod(cf *CompanyFacts, conceptName string, end time.Time) bool {
	facts, ok := cf.Facts[conceptName]
	if !ok || len(facts) == 0 {
		return false
	}

	for i := range facts {
		if facts[i].End.Equal(end) {
			return true
		}
	}

	return false
}

// latestFiledForConceptPeriodForm returns the latest Filed date for matching
// facts. Used to allow synthesis of restated comparative values from later
// filings: the check is "did a newer filing already process this period?".
func latestFiledForConceptPeriodForm(cf *CompanyFacts, conceptName string, end time.Time, formType string) (time.Time, bool) {
	facts, ok := cf.Facts[conceptName]
	if !ok || len(facts) == 0 {
		return time.Time{}, false
	}

	var latest time.Time

	found := false

	for i := range facts {
		if facts[i].End.Equal(end) && facts[i].Form == formType {
			if !found || facts[i].Filed.After(latest) {
				latest = facts[i].Filed
				found = true
			}
		}
	}

	return latest, found
}

// hasFactForPeriodStartAndForm reports whether a matching fact exists. Use
// when distinguishing same-end-date facts with different durations (e.g. Q3
// 10-Q reports both single-quarter and YTD WAS facts ending 2024-09-30).
func hasFactForPeriodStartAndForm(cf *CompanyFacts, conceptName string, end, start time.Time, formType string) bool {
	facts, ok := cf.Facts[conceptName]
	if !ok || len(facts) == 0 {
		return false
	}

	for i := range facts {
		if facts[i].End.Equal(end) && facts[i].Start.Equal(start) && facts[i].Form == formType {
			return true
		}
	}

	return false
}

// parseXBRLInstanceExtensions parses an XBRL instance XML document and
// extracts extension (non-us-gaap/dei) facts, plus dimensional us-gaap
// share/EPS facts for multi-class filers and gap-fill us-gaap facts the
// companyfacts API omits.
func parseXBRLInstanceExtensions(xmlData []byte, cf *CompanyFacts, filed time.Time, formType string) {
	decoder := xml.NewDecoder(bytes.NewReader(xmlData))
	// Inline XBRL instance XML may contain HTML entities; be lenient.
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	contexts := make(map[string]contextPeriod)

	// We need to scan the entire document to build contexts, then re-scan
	// for facts. To avoid two passes over the byte stream, collect raw
	// fact data during the first pass.

	var rawFacts []rawFact

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}

		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch se.Name.Local {
		case "context":
			parseXBRLContext(decoder, &se, contexts)

		default:
			ns := se.Name.Space
			conceptName := se.Name.Local

			// Determine if this is a us-gaap share concept we want from
			// dimensional contexts, or an extension (non-standard) concept.
			isStdNS := ns == "" ||
				strings.Contains(ns, "us-gaap") ||
				strings.Contains(ns, "/dei/") ||
				strings.Contains(ns, "xbrl.org") ||
				strings.Contains(ns, "w3.org") ||
				strings.Contains(ns, "xbrl.sec.gov/ecd")
			isShareConcept := isStdNS && dimensionalShareConcepts[conceptName]

			// Standard us-gaap concepts that aren't share concepts are
			// captured as gap-fill: the companyfacts API sometimes omits
			// consolidated facts for conglomerates and insurance companies
			// even though they appear in the inline XBRL.
			isGapFill := isStdNS && !isShareConcept

			var contextRef, unitRef, signAttr string

			for _, attr := range se.Attr {
				switch attr.Name.Local {
				case "contextRef":
					contextRef = attr.Value
				case "unitRef":
					unitRef = attr.Value
				case "sign":
					signAttr = attr.Value
				}
			}

			if contextRef == "" || unitRef == "" {
				continue
			}

			// Read the element text content.
			var content string

			for {
				innerTok, err := decoder.Token()
				if err != nil {
					break
				}

				if cd, ok := innerTok.(xml.CharData); ok {
					content += string(cd)
				}

				if end, ok := innerTok.(xml.EndElement); ok {
					if end.Name.Local == se.Name.Local {
						break
					}
				}
			}

			content = strings.TrimSpace(content)
			if content == "" {
				continue
			}

			val, err := strconv.ParseFloat(content, 64)
			if err != nil {
				continue
			}

			// Inline XBRL sign="-" means the displayed value is the negation
			// of the reported amount (e.g. GS tags a repayment as sign="-"
			// on a credit-balance concept so the presented figure matches
			// the cash outflow direction on the statement).
			if signAttr == "-" {
				val = -val
			}

			rawFacts = append(rawFacts, rawFact{
				conceptName:    conceptName,
				contextRef:     contextRef,
				unitRef:        unitRef,
				value:          val,
				isShareConcept: isShareConcept,
				isGapFill:      isGapFill,
			})
		}
	}

	// Inputs collected during the matching loop to synthesize a
	// WeightedAverageNumberOfDilutedSharesOutstanding fact for dual-class
	// filers that file per-class Basic WAS but omit the Diluted tag.
	type dilutedSynthInput struct {
		end, start time.Time
		class      string
		val        float64
	}

	var dilutedSynthInputs []dilutedSynthInput

	// Second step: match facts to contexts and build Fact objects.
	for _, rf := range rawFacts {
		ctx, ok := contexts[rf.contextRef]
		if !ok {
			continue
		}

		if rf.isShareConcept {
			// Share/EPS concepts: capture from dimensional contexts only
			// (non-dimensional values are already in companyfacts).
			// For per-share and weighted-average concepts, only Class B
			// (equivalent) values are meaningful — Class A values are in a
			// different unit scale. For share COUNT concepts, both Class A
			// and Class B raw counts must be captured so they can be summed
			// downstream (resolveSharesBasicAsOf).
			if !ctx.hasDim {
				continue
			}

			// Before filtering to Class B for per-share/WAS concepts, for
			// WeightedAverageNumberOfSharesOutstandingBasic only, capture
			// the Class A raw WAS fact into the diluted-synthesis bucket.
			// Sharadar's weighted_average_shares_diluted for dual-class
			// filers equals ClassA_raw_WAS + ClassB_equivalent_WAS (which
			// itself already includes Class A converted to B-equivalents
			// plus raw Class B). BRK does not file
			// WeightedAverageNumberOfDilutedSharesOutstanding, so the
			// fallback chain otherwise lands on the Class B basic value
			// and underreports diluted by exactly ClassA_raw_WAS.
			if rf.conceptName == "WeightedAverageNumberOfSharesOutstandingBasic" &&
				contextHasClassAMember(ctx) {
				dilutedSynthInputs = append(dilutedSynthInputs, dilutedSynthInput{
					end:   ctx.end,
					start: ctx.start,
					class: "A",
					val:   rf.value,
				})
			}

			if multiClassShareCountConcepts[rf.conceptName] {
				// Detect equivalent (aggregated) contexts vs. raw per-class
				// contexts. Equivalent members (e.g. brka:EquivalentClassA/
				// BMember) are aggregated totals expressed in one class's
				// unit scale (A*1500+B for BRK); skip them so we never sum
				// aggregated+raw per-class values for the same filing.
				isEquivalent := false
				classLabel := ""

				for _, m := range ctx.dimMembers {
					if strings.Contains(m, "Equivalent") {
						isEquivalent = true

						break
					}

					if classLabel == "" {
						switch {
						case strings.Contains(m, "ClassA"):
							classLabel = "A"
						case strings.Contains(m, "ClassB"):
							classLabel = "B"
						}
					}
				}

				if isEquivalent {
					continue
				}

				// Capture ClassA/ClassB raw per-class counts for the
				// market-ratio share_factor formula. Non-A/B dimensional
				// contexts (e.g. CALM's regular CommonStockMember alongside
				// a minor CommonClassAMember) still fall through to the
				// cf.Facts append below so resolveSharesBasicAsOf sums all
				// per-class raw counts from the same filing.
				if classLabel != "" {
					cf.ClassShares = append(cf.ClassShares, ClassSharesFact{
						Filed:   filed,
						End:     ctx.end,
						Form:    formType,
						Concept: rf.conceptName,
						Class:   classLabel,
						Val:     rf.value,
					})
				}
			} else if !contextHasClassBMember(ctx) {
				continue
			}

			if rf.conceptName == "WeightedAverageNumberOfSharesOutstandingBasic" &&
				contextHasClassBMember(ctx) {
				dilutedSynthInputs = append(dilutedSynthInputs, dilutedSynthInput{
					end:   ctx.end,
					start: ctx.start,
					class: "B",
					val:   rf.value,
				})
			}
		} else if rf.isGapFill {
			// Standard us-gaap gap-fill: only from non-dimensional contexts,
			// and only when the companyfacts API doesn't already have a fact
			// for this concept and period end date.
			if ctx.hasDim {
				continue
			}

			if hasFactForPeriod(cf, rf.conceptName, ctx.end) {
				continue
			}
		} else {
			// Extension facts: skip dimensional contexts (as before).
			if ctx.hasDim {
				continue
			}
		}

		fact := Fact{
			End:   ctx.end,
			Start: ctx.start,
			Val:   rf.value,
			Filed: filed,
			Form:  formType,
		}

		cf.Facts[rf.conceptName] = append(cf.Facts[rf.conceptName], fact)
	}

	// Synthesize WeightedAverageNumberOfDilutedSharesOutstanding from the
	// Class A + Class B per-class WeightedAverageNumberOfSharesOutstandingBasic
	// facts collected above. Only runs when a dual-class filing supplied both
	// class facts for a period and the companyfacts API has no non-dimensional
	// Diluted fact for that period — so companies that report Diluted directly
	// are never overridden.
	if len(dilutedSynthInputs) > 0 {
		type periodKey struct {
			end, start time.Time
		}

		byPeriod := make(map[periodKey]map[string]float64)

		for _, in := range dilutedSynthInputs {
			pk := periodKey{end: in.end, start: in.start}
			if byPeriod[pk] == nil {
				byPeriod[pk] = make(map[string]float64)
			}

			byPeriod[pk][in.class] = in.val
		}

		for pk, classVals := range byPeriod {
			a, hasA := classVals["A"]
			b, hasB := classVals["B"]

			if !hasA || !hasB {
				continue
			}

			// Guard on (end, start, form) not just (end, form). BRK's Q3 10-Q
			// reports both a single-quarter (90-day) and a YTD (273-day)
			// WAS_basic per-class; we synthesize two distinct Diluted facts
			// for the same end date but different durations. The longer YTD
			// cumulative is required by Q4 period-average synthesis for
			// day-weighted math; dropping it causes Q4 to diverge by ~0.19%.
			if hasFactForPeriodStartAndForm(cf, "WeightedAverageNumberOfDilutedSharesOutstanding", pk.end, pk.start, formType) {
				continue
			}

			cf.Facts["WeightedAverageNumberOfDilutedSharesOutstanding"] = append(
				cf.Facts["WeightedAverageNumberOfDilutedSharesOutstanding"],
				Fact{End: pk.end, Start: pk.start, Val: a + b, Filed: filed, Form: formType},
			)
		}
	}

	// Synthesize consolidated facts from dimensional data for concepts
	// that have no non-dimensional data in CompanyFacts.
	synthesizeConsolidatedFacts(cf, rawFacts, contexts, filed, formType)

	// Sort facts by filing date. Use the map key to access the current
	// slice — the range variable is a copy of the slice header and may
	// be stale if synthesizeConsolidatedFacts replaced the slice.
	for concept := range cf.Facts {
		sort.SliceStable(cf.Facts[concept], func(i, j int) bool {
			return cf.Facts[concept][i].Filed.Before(cf.Facts[concept][j].Filed)
		})
	}
}

// synthesizeConsolidatedFacts creates consolidated facts from dimensional
// segment data for concepts missing from CompanyFacts. Conglomerates report
// some line items only with segment dimensions; we sum top-level segments
// along the product/service axis, excluding sub-segments.
//
// singleSegmentAllowed lists concepts where one segment alone represents the
// consolidated total (the other segment uses a different cost taxonomy).
var singleSegmentAllowed = map[string]bool{
	"SellingGeneralAndAdministrativeExpense":                   true,
	"IntangibleAssetsNetExcludingGoodwill":                     true,
	"PremiumsAndOtherReceivablesNet":                           true,
	"UnearnedPremiums":                                         true,
	"USTreasuryBills":                                          true, // BRK extension: only reported under InsuranceAndOther segment
	"OtherExpenses":                                            true, // BRK: Railroad segment other expenses (above-the-line, non-SGA)
	"ProceedsFromIssuanceOfLongTermDebt":                       true, // BRK: only Railroad segment issues LT debt
	"AccountsAndOtherReceivablesNet":                           true, // BRK (brka:) extension: Railroad/Utilities trade receivables
	"AircraftRepurchaseLiabilitiesAndDeferredRevenueLeasesNet": true, // BRK (brka:) NetJets deferred lease revenue
	"InterestExpenseNonoperating":                              true, // CAT: only reported under AllOtherExcludingFinancialProducts segment (financial-products interest is above the line)
	"ShortTermBorrowings":                                      true, // CAT: only reported under FinancialProducts segment on 10-Q balance sheet (MET has no ST borrowings)
	"ProceedsFromDebtMaturingInMoreThanThreeMonths":            true, // CAT: 10-K dimensional (only FP segment; MET has no LT debt issuance)
}

// extensionSynthesisConcepts whitelists extension (non-us-gaap) concepts for
// dimensional synthesis because they exist only in dimensional contexts.
var extensionSynthesisConcepts = map[string]bool{
	"USTreasuryBills":                                          true,
	"AccountsAndOtherReceivablesNet":                           true, // BRK (brka:) extension: Railroad/Utilities trade receivables
	"AircraftRepurchaseLiabilitiesAndDeferredRevenueLeasesNet": true, // BRK (brka:): NetJets deferred lease revenue
}

func synthesizeConsolidatedFacts(cf *CompanyFacts, rawFacts []rawFact, contexts map[string]contextPeriod, filed time.Time, formType string) {
	type periodKey struct {
		concept string
		end     time.Time
	}

	type dimFact struct {
		val    float64
		start  time.Time
		member string // dimension member value for deduplication
		isFlow bool   // true for duration facts (income/cash flow), false for instant (balance sheet)
	}

	groups := make(map[periodKey][]dimFact)

	for _, rf := range rawFacts {
		// Process us-gaap gap-fill facts and whitelisted extension concepts.
		if !rf.isGapFill && !extensionSynthesisConcepts[rf.conceptName] {
			continue
		}

		ctx, ok := contexts[rf.contextRef]
		if !ok || !ctx.hasDim {
			continue
		}

		// Only facts with exactly one dimension on the ProductOrServiceAxis.
		if len(ctx.dimMembers) != 1 || len(ctx.dimAxes) != 1 {
			continue
		}

		if !strings.Contains(ctx.dimAxes[0], "ProductOrServiceAxis") {
			continue
		}

		// For preferred-USD concepts, skip when the API already has a fact
		// filed on or after this one. Re-synthesize when the current filing
		// is newer so comparative restatements get captured. For
		// non-preferred-unit concepts always collect so synthesis can
		// replace foreign-currency placeholders.
		if !cf.NonPreferredUnitConcepts[rf.conceptName] {
			if latestFiled, ok := latestFiledForConceptPeriodForm(cf, rf.conceptName, ctx.end, formType); ok {
				if !filed.After(latestFiled) {
					continue
				}
			}
		}

		pk := periodKey{concept: rf.conceptName, end: ctx.end}
		groups[pk] = append(groups[pk], dimFact{
			val:    rf.value,
			start:  ctx.start,
			member: ctx.dimMembers[0],
			isFlow: !ctx.start.IsZero(),
		})
	}

	const tolerance = 0.02

	for pk, facts := range groups {
		// Deduplicate by member: same member+period can appear in multiple
		// notes within one filing. Keep the first occurrence.
		seen := make(map[string]bool)
		deduped := facts[:0]

		for _, f := range facts {
			if seen[f.member] {
				continue
			}

			seen[f.member] = true

			deduped = append(deduped, f)
		}

		facts = deduped

		if len(facts) < 2 && !singleSegmentAllowed[pk.concept] {
			continue
		}

		if len(facts) == 0 {
			continue
		}

		// Detect sub-segments: if fact[j] + fact[k] ≈ fact[i], then j and k
		// are sub-segments of i. Mark j and k as children.
		isChild := make([]bool, len(facts))

		for i, parent := range facts {
			if parent.val == 0 {
				continue
			}

			for j := range facts {
				if j == i {
					continue
				}

				for k := j + 1; k < len(facts); k++ {
					if k == i {
						continue
					}

					pairSum := facts[j].val + facts[k].val
					if pairSum != 0 && math.Abs(pairSum-parent.val)/math.Abs(parent.val) < tolerance {
						isChild[j] = true
						isChild[k] = true
					}
				}
			}
		}

		var (
			sum   float64
			count int
			start time.Time
		)

		for i, f := range facts {
			if isChild[i] {
				continue
			}

			sum += f.val
			count++

			if !f.start.IsZero() {
				start = f.start
			}
		}

		// A single-segment value is allowed only for whitelisted concepts.
		if count < 2 && !singleSegmentAllowed[pk.concept] {
			continue
		}

		newFact := Fact{
			End:   pk.end,
			Start: start,
			Val:   sum,
			Filed: filed,
			Form:  formType,
		}

		// For non-preferred-unit concepts, replace existing same-period+form
		// facts (the API may have loaded foreign-currency placeholders).
		// Preserve the earliest original filing date so the synthesized fact
		// passes the same filing-date filters.
		if count >= 2 && cf.NonPreferredUnitConcepts[pk.concept] {
			filtered := make([]Fact, 0, len(cf.Facts[pk.concept]))
			earliestFiled := filed

			for _, f := range cf.Facts[pk.concept] {
				if f.End.Equal(pk.end) && f.Form == formType {
					if f.Filed.Before(earliestFiled) {
						earliestFiled = f.Filed
					}
				} else {
					filtered = append(filtered, f)
				}
			}

			newFact.Filed = earliestFiled
			cf.Facts[pk.concept] = append(filtered, newFact)
		} else {
			cf.Facts[pk.concept] = append(cf.Facts[pk.concept], newFact)
		}
	}
}

// contextPeriod holds period and dimension info for an XBRL context.
type contextPeriod struct {
	start      time.Time
	end        time.Time
	hasDim     bool
	dimMembers []string // dimension member values (e.g. "brka:EquivalentClassBMember")
	dimAxes    []string // dimension axis names (e.g. "srt:ProductOrServiceAxis")
}

// parseXBRLContext parses a single <xbrli:context> element from the XML stream.
func parseXBRLContext(decoder *xml.Decoder, se *xml.StartElement, contexts map[string]contextPeriod) {
	var ctxID string

	for _, attr := range se.Attr {
		if attr.Name.Local == "id" {
			ctxID = attr.Value
		}
	}

	if ctxID == "" {
		return
	}

	var cp contextPeriod

	depth := 1

	for depth > 0 {
		tok, err := decoder.Token()
		if err != nil {
			return
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++

			switch t.Name.Local {
			case "instant":
				if text, err := readElementText(decoder); err == nil {
					cp.end, _ = time.Parse("2006-01-02", text)
				}

				depth-- // readElementText consumed the end element
			case "startDate":
				if text, err := readElementText(decoder); err == nil {
					cp.start, _ = time.Parse("2006-01-02", text)
				}

				depth--
			case "endDate":
				if text, err := readElementText(decoder); err == nil {
					cp.end, _ = time.Parse("2006-01-02", text)
				}

				depth--
			case "explicitMember":
				cp.hasDim = true

				// Capture the dimension axis name from the "dimension" attribute.
				var dimAttr string

				for _, attr := range t.Attr {
					if attr.Name.Local == "dimension" {
						dimAttr = attr.Value
					}
				}

				cp.dimAxes = append(cp.dimAxes, dimAttr)

				if memberText, err := readElementText(decoder); err == nil {
					cp.dimMembers = append(cp.dimMembers, memberText)
				}

				depth-- // readElementText consumed the end element
			}

		case xml.EndElement:
			depth--
		}
	}

	if !cp.end.IsZero() {
		contexts[ctxID] = cp
	}
}

// readElementText reads the text content of the current element and consumes
// its end tag.
func readElementText(decoder *xml.Decoder) (string, error) {
	var text string

	for {
		tok, err := decoder.Token()
		if err != nil {
			return "", err
		}

		if cd, ok := tok.(xml.CharData); ok {
			text += string(cd)
		}

		if _, ok := tok.(xml.EndElement); ok {
			return strings.TrimSpace(text), nil
		}
	}
}

// DownloadCompanyFactsZip downloads and extracts companyfacts.zip, calling
// processFn for each CIK JSON. Streams to a temp file to avoid loading the
// ~1GB archive into memory. If localZipPath is set, uses the local file.
func DownloadCompanyFactsZip(ctx context.Context, client *resty.Client, localZipPath string, processFn func(cik int, jsonData []byte) error) error {
	zipPath := localZipPath
	removeTmp := false

	if zipPath == "" {
		log.Info().Str("url", companyFactsZipURL).Msg("downloading companyfacts.zip from SEC (this may take several minutes)")

		tmpFile, err := os.CreateTemp("", "sec-companyfacts-*.zip")
		if err != nil {
			return fmt.Errorf("creating temp file for companyfacts.zip: %w", err)
		}

		zipPath = tmpFile.Name()
		removeTmp = true

		resp, err := client.R().
			SetContext(ctx).
			SetDoNotParseResponse(true).
			Get(companyFactsZipURL)
		if err != nil {
			if closeErr := tmpFile.Close(); closeErr != nil {
				log.Warn().Err(closeErr).Str("file", zipPath).Msg("error closing temp file")
			}

			return fmt.Errorf("downloading companyfacts.zip: %w", err)
		}

		rawBody := resp.RawBody()

		defer func() {
			if closeErr := rawBody.Close(); closeErr != nil {
				log.Warn().Err(closeErr).Msg("error closing companyfacts.zip response body")
			}
		}()

		if resp.StatusCode() != http.StatusOK {
			if closeErr := tmpFile.Close(); closeErr != nil {
				log.Warn().Err(closeErr).Str("file", zipPath).Msg("error closing temp file")
			}

			return fmt.Errorf("SEC returned status %d for companyfacts.zip", resp.StatusCode())
		}

		if _, err := io.Copy(tmpFile, rawBody); err != nil {
			if closeErr := tmpFile.Close(); closeErr != nil {
				log.Warn().Err(closeErr).Str("file", zipPath).Msg("error closing temp file")
			}

			return fmt.Errorf("streaming companyfacts.zip to temp file: %w", err)
		}

		if err := tmpFile.Close(); err != nil {
			return fmt.Errorf("closing temp file for companyfacts.zip: %w", err)
		}
	} else {
		log.Info().Str("path", zipPath).Msg("using local companyfacts.zip")
	}

	if removeTmp {
		defer func() {
			if removeErr := os.Remove(zipPath); removeErr != nil && !os.IsNotExist(removeErr) {
				log.Warn().Err(removeErr).Str("file", zipPath).Msg("error removing temp file")
			}
		}()
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("opening companyfacts.zip: %w", err)
	}

	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("error closing companyfacts.zip reader")
		}
	}()

	for _, f := range reader.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if filepath.Ext(f.Name) != ".json" {
			continue
		}

		// Extract CIK from filename (e.g., "CIK0000320193.json")
		base := strings.TrimSuffix(filepath.Base(f.Name), ".json")
		base = strings.TrimPrefix(base, "CIK")

		cik, err := strconv.Atoi(base)
		if err != nil {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("error opening file in zip")
			continue
		}

		jsonData, err := io.ReadAll(rc)

		if closeErr := rc.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("file", f.Name).Msg("error closing file in zip")
		}

		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("error reading file in zip")
			continue
		}

		if err := processFn(cik, jsonData); err != nil {
			log.Warn().Err(err).Int("cik", cik).Msg("error processing companyfacts")
		}
	}

	return nil
}

// NewSECClient creates a resty HTTP client configured for SEC EDGAR API access.
func NewSECClient(userAgent string, limiter *rate.Limiter) *resty.Client {
	client := resty.New().
		SetHeader("User-Agent", userAgent).
		SetHeader("Accept", "application/json").
		SetTimeout(60 * time.Second).
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return r != nil && (r.StatusCode() == http.StatusTooManyRequests || r.StatusCode() >= 500)
		}).
		OnBeforeRequest(func(_ *resty.Client, r *resty.Request) error {
			return limiter.Wait(r.Context())
		})

	return client
}
