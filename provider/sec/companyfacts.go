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
	// Accn is the SEC accession number identifying the filing that reported
	// this fact. It is currently parsed for completeness but not used for
	// dedup or correlation. A future enhancement could key on Accn to detect
	// "this exact filing was already processed" at the fact level, or to
	// group facts reported together in the same filing for provenance
	// tracking.
	Accn  string // SEC accession number
	Form  string // Filing form type (10-K, 10-Q)
	FP    string // Fiscal period (FY, Q1, Q2, Q3, Q4)
	Frame string // XBRL frame identifier (e.g. CY2023Q3I)
	FY    int    // Fiscal year
}

// ClassSharesFact captures a Class A or Class B raw cover-page share count
// from a specific filing. Used for the market-ratio share_factor formula in
// multi-class filers: share_factor = (A*A_price + B*B_price) / ((A+B)*our_price).
// Only raw CommonClassAMember / CommonClassBMember contexts are captured —
// equivalent (B-unit) totals like EquivClassAMember are tracked elsewhere.
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

	// NonPreferredUnitConcepts tracks concepts loaded from non-preferred
	// units (e.g. EUR, GBP, JPY instead of USD). These values may be
	// foreign-currency denominations rather than consolidated USD totals.
	// Dimensional synthesis can replace these with correct segment sums.
	NonPreferredUnitConcepts map[string]bool

	// ClassShares collects Class A and Class B cover-page share counts from
	// dual-class filings. Populated during inline XBRL parsing whenever
	// EntityCommonStockSharesOutstanding or CommonStockSharesOutstanding is
	// reported on a context with a CommonClassAMember or CommonClassBMember.
	// Used by resolveClassSharesAsOf for the market-ratio share_factor.
	ClassShares []ClassSharesFact
}

// FilterByFilingDate removes all facts that were filed after the given cutoff
// date. This is used to reproduce the data view at a historical point in time,
// e.g. to compare SEC-derived fundamentals against a Sharadar snapshot taken
// on a specific date.
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

// ParseCompanyFacts parses SEC EDGAR companyfacts JSON into a CompanyFacts struct.
// It only includes facts from 10-K and 10-Q filings and selects the preferred
// unit type when multiple are available for a concept.
//
// All XBRL namespaces present in the JSON are parsed, including:
//   - "us-gaap": financial statement data (balance sheet, income, cash flow)
//   - "dei": filing metadata (EntityCommonStockSharesOutstanding, etc.)
//   - Company extensions (e.g. "msft", "aapl"): custom concepts that companies
//     define for line items not covered by us-gaap, such as MSFT's
//     DepreciationAmortizationAndOther cash flow line.
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

	// Parse ALL namespaces present in the facts object, not just us-gaap
	// and dei. Company extension namespaces contain custom concepts for
	// line items that aren't covered by standard taxonomies.
	root.Get("facts").ForEach(func(nsName, nsData gjson.Result) bool {
		parseNamespaceFacts(nsData, cf)
		return true
	})

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

		// If no preferred unit found, try the first available unit.
		// Some concepts (e.g. BRK's DebtAndCapitalLeaseObligations) only
		// have foreign-currency units in the API. We load them as
		// placeholders; dimensional synthesis will replace them with
		// correct consolidated values when inline XBRL data is available.
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
			// given filing date. This is a one-time cost per concept that
			// avoids O(N) scans on every (period, filing-date) lookup.
			//
			// Use a stable sort so facts filed on the same day preserve their
			// JSON parse order; this keeps downstream resolution deterministic
			// when multiple facts share a filing date (e.g. comparative
			// balance-sheet entries reported in the same 10-K).
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

// EnrichWithExtensionFacts fetches the company's recent 10-K/10-Q inline XBRL
// filings from SEC EDGAR and parses extension facts (company-specific XBRL
// concepts not in us-gaap or dei). These are added to cf.Facts alongside the
// standard facts already parsed from companyfacts JSON.
//
// Extension concepts are used by companies for cash flow line items like
// "Depreciation, amortization, and other" (msft:DepreciationAmortizationAndOther)
// that aren't covered by us-gaap taxonomy concepts.
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

	// Collect recent 10-K and 10-Q filings for extension enrichment. We need
	// enough history so that Q4 synthesis for the prior fiscal year uses
	// consistent extension facts. The submissions list is reverse-chronological,
	// so we collect filings until we see the THIRD 10-K (giving us two full
	// fiscal years of quarterly filings plus their annual summaries).
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

	// If we haven't found enough 10-K filings, load overflow submission
	// files. Large filers (e.g. JPM with 23K+ filings/year) push older
	// 10-K/10-Q filings into overflow files.
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

	// Reverse so filings are processed in chronological order (oldest first).
	// This ensures the original 10-Q's dimensional synthesis facts are
	// established before later filings' comparative data is processed.
	// Without this, a Q1-2025 10-Q's restated Q1-2024 comparative would
	// block the original Q1-2024 synthesis (hasFactForPeriodAndForm).
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

// dimensionalShareConcepts lists us-gaap concepts for per-share and share
// count data that some multi-class filers (e.g. BRK/B) report ONLY with
// dimensional XBRL (one value per share class). The companyfacts API
// excludes dimensional facts, so they must be captured from the inline XBRL
// instance when the context carries a "ClassB" member.
var dimensionalShareConcepts = map[string]bool{
	"WeightedAverageNumberOfSharesOutstandingBasic":          true,
	"WeightedAverageNumberOfDilutedSharesOutstanding":        true,
	"WeightedAverageNumberOfShareOutstandingBasicAndDiluted": true,
	"EarningsPerShareBasic":                                  true,
	"EarningsPerShareDiluted":                                true,
	"CommonStockSharesOutstanding":                           true,
	"EntityCommonStockSharesOutstanding":                     true, // DEI cover-page shares; multi-class filers report per-class
}

// contextHasClassBMember returns true if any dimension member in the context
// contains "ClassB" (matching both standard us-gaap:CommonClassBMember and
// company extensions like brka:EquivalentClassBMember).
func contextHasClassBMember(ctx contextPeriod) bool {
	for _, m := range ctx.dimMembers {
		if strings.Contains(m, "ClassB") {
			return true
		}
	}

	return false
}

// contextHasClassAMember returns true if any dimension member in the context
// contains "ClassA" (matching us-gaap:CommonClassAMember and company
// extensions). Used alongside contextHasClassBMember for share count concepts
// where both class totals must be captured and summed (e.g. BRK/B's
// EntityCommonStockSharesOutstanding is filed per-class in raw share units).
func contextHasClassAMember(ctx contextPeriod) bool {
	for _, m := range ctx.dimMembers {
		if strings.Contains(m, "ClassA") {
			return true
		}
	}

	return false
}

// multiClassShareCountConcepts lists share-count concepts whose per-class
// values are raw share counts that must be summed across classes to match
// Sharadar's shares_basic. Per-share (EPS) and weighted-average-shares
// concepts are NOT in this set: Sharadar reports those in Class B
// (equivalent) units, so only the Class B dimensional value is captured.
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

// hasFactForPeriod returns true if CompanyFacts already contains at least one
// fact for the given concept name and period end date. This is used to avoid
// duplicating data that the companyfacts API already provides.
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

// hasFactForPeriodAndForm returns true if CompanyFacts already contains a fact
// matching the concept, period end, and form type.
func hasFactForPeriodAndForm(cf *CompanyFacts, conceptName string, end time.Time, formType string) bool {
	facts, ok := cf.Facts[conceptName]
	if !ok || len(facts) == 0 {
		return false
	}

	for i := range facts {
		if facts[i].End.Equal(end) && facts[i].Form == formType {
			return true
		}
	}

	return false
}

// latestFiledForConceptPeriodForm returns the latest Filed date among existing
// facts for the given concept, period end, and form type. Returns the zero
// time and false when no such fact exists. Used to allow synthesis of
// restated comparative values from later filings — the check is "did a newer
// filing already process this period?" rather than "does ANY prior fact exist?".
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

// hasFactForPeriodStartAndForm returns true if CompanyFacts already contains a
// fact matching the concept, period end, period start, and form type. Use this
// instead of hasFactForPeriodAndForm when distinguishing between facts that
// share the same end date but differ in duration (e.g. the Q3 10-Q reports
// both a single-quarter 90-day WAS fact and a 273-day YTD WAS fact — both end
// 2024-09-30 but have different starts).
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
// extracts extension facts (concepts not in us-gaap or dei namespaces).
// It also captures us-gaap share/EPS concepts from dimensional contexts
// with a Class B member, since multi-class filers may only report these
// dimensionally (not in the companyfacts API). Additionally, it captures
// standard us-gaap concepts from non-dimensional contexts when the
// companyfacts API is missing data for that concept and period (gap-fill).
// It uses encoding/xml for proper XML parsing.
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

			// Standard us-gaap concepts that are NOT share concepts are
			// captured as gap-fill candidates. The companyfacts API sometimes
			// omits consolidated facts for conglomerates and insurance
			// companies (e.g. BRK/B's PropertyPlantAndEquipmentNet,
			// CostOfGoodsAndServicesSold). These facts exist in the inline
			// XBRL but are absent from the API. We capture them from
			// non-dimensional contexts and only add them when no existing
			// fact covers the same period.
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
				if !contextHasClassAMember(ctx) && !contextHasClassBMember(ctx) {
					continue
				}

				// Capture raw per-class cover counts for the market-ratio
				// share_factor formula. Only un-equivalized us-gaap:
				// CommonClassA/BMember contexts carry raw counts — brka:
				// EquivalentClassA/BMember variants are aggregated totals
				// expressed in B-unit terms (A*1500+B) and must be skipped.
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

				if !isEquivalent && classLabel != "" {
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

// synthesizeConsolidatedFacts creates consolidated (non-dimensional) facts from
// dimensional segment data for concepts missing from CompanyFacts. Conglomerates
// like BRK/B report certain balance sheet and income statement items only with
// segment dimensions (e.g. srt:ProductOrServiceAxis with InsuranceAndOther and
// RailroadUtilitiesAndEnergy members). The consolidated value is the sum of
// top-level segment totals along the product/service axis.
//
// Only facts with exactly one dimension member on the srt:ProductOrServiceAxis
// are considered. Sub-segments (whose values sum to a parent segment's value)
// are detected and excluded to avoid double-counting.
// singleSegmentAllowed lists concepts where a single ProductOrServiceAxis
// segment value represents the consolidated total. These are items reported
// only by one segment because the other segment uses different cost categories.
// For example, conglomerates report SGA under Insurance while operating
// subsidiaries classify overhead as railroad/utility operating expenses.
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
}

// extensionSynthesisConcepts lists extension (non-us-gaap) concept names that
// should be eligible for dimensional synthesis in synthesizeConsolidatedFacts.
// Normally only us-gaap gap-fill concepts are processed; these extension
// concepts are added because they exist only in dimensional contexts.
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

		// For concepts with preferred USD units in the API, skip synthesis
		// when the API already has data for this period+form that was
		// filed on or after the current filing. The prior fact is still
		// authoritative. When the CURRENT filing is NEWER than any existing
		// fact, re-synthesize so comparative restatements are captured —
		// e.g. BRK's Q1 2025 10-Q reclassifies Q1 2024 SGA by +768M into
		// Insurance and Other. Without this, our MR resolution is stuck on
		// the original 10-Q's value and Sharadar's MRQ (which picks up the
		// restated comparative) doesn't match. For non-preferred-unit
		// concepts (e.g. BRK's DebtAndCapitalLeaseObligations in EUR),
		// always collect so synthesis can replace with correct segment
		// sums.
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
		// Deduplicate by member: same member+period can appear multiple
		// times in a filing (balance sheet vs. segment note). Keep the
		// first occurrence.
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

		var sum float64
		var count int
		var start time.Time

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

		// Some concepts are correctly represented by a single segment when
		// the other segment doesn't report this item (different cost taxonomy).
		// For example, SGA and intangibles-ex-goodwill in conglomerates are
		// reported only under the Insurance segment because operating
		// subsidiaries classify their overhead under segment-specific tags.
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

		// When synthesis produces a multi-segment value (count >= 2) for
		// a non-preferred-unit concept, replace ALL existing facts for
		// the same period+form. The API may have loaded facts from
		// foreign-currency units (e.g. BRK's EUR-denominated debt).
		// Preserve the earliest original filing date so the synthesized
		// fact passes the same filing-date filters as the original.
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

// DownloadCompanyFactsZip downloads and extracts the bulk companyfacts.zip file,
// calling processFn for each individual CIK JSON file. The download is streamed
// to a temp file on disk to avoid loading the entire ~1GB archive into memory;
// the zip itself requires random access (its central directory is at the end of
// the file), so it is then opened from the temp file using zip.OpenReader.
//
// If localZipPath is non-empty, the local file is used directly instead of
// downloading from SEC. This is useful during development to avoid re-downloading
// the ~1GB file on every run.
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
