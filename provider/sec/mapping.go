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
	"math"
	"time"
)

// ResolveDirect searches CompanyFacts for the first tag in m.XBRLTags that
// has a fact matching periodEnd and formType. When multiple facts match,
// single-quarter wins over cumulative, then latest filing, then shortest
// duration.
func ResolveDirect(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) (float64, bool) {
	if val, ok := resolveDirectForm(cf, m, periodEnd, formType); ok {
		return val, true
	}

	// Cross-form fallback: some filers only tag certain balance-sheet
	// concepts on 10-Q (MCD's operating lease liabilities are silent on
	// the 10-K).
	if m.AllowCrossFormFallback {
		otherForm := "10-Q"
		if formType == "10-Q" {
			otherForm = "10-K"
		}

		if val, ok := resolveDirectForm(cf, m, periodEnd, otherForm); ok {
			return val, true
		}
	}

	return 0, false
}

func resolveDirectForm(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) (float64, bool) {
	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	// Normalize once so we match facts whose raw end date differs by a day
	// or two but belongs to the same fiscal period.
	normalPeriodEnd := NormalizeEventDate(periodEnd, formType)

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		// Find the best matching fact for this period
		var best *Fact

		for i := range facts {
			f := &facts[i]

			// Must match the normalized period end date
			if !NormalizeEventDate(f.End, formType).Equal(normalPeriodEnd) {
				continue
			}

			// Must match the form type
			if f.Form != formType {
				continue
			}

			// For duration concepts, verify the period length is reasonable.
			if !f.Start.IsZero() {
				days := f.End.Sub(f.Start).Hours() / 24
				if formType == "10-K" && days < 300 {
					continue // Skip quarterly data in an annual filing
				}
			} else if m.StatementType == StmtFlow {
				// Instant-style facts (zero Start) aren't meaningful for
				// flow concepts: some filers tag dividend amounts with the
				// declaration date as End and no Start, and those would
				// otherwise compete with valid duration facts.
				continue
			}

			// Single-quarter beats cumulative regardless of filing date:
			// comparative filings often include only the cumulative value,
			// and preferring latest-filed would lose the original
			// single-quarter fact.
			if best == nil {
				best = f
			} else {
				fDays := f.End.Sub(f.Start).Hours() / 24
				bestDays := best.End.Sub(best.Start).Hours() / 24
				fIsQuarterly := !f.Start.IsZero() && fDays <= ytdThresholdDays
				bestIsQuarterly := !best.Start.IsZero() && bestDays <= ytdThresholdDays

				switch {
				case fIsQuarterly && !bestIsQuarterly:
					// Single-quarter always wins over cumulative.
					best = f
				case !fIsQuarterly && bestIsQuarterly:
					// Keep the single-quarter fact.
				case f.Filed.After(best.Filed):
					// Same duration category: prefer latest filing.
					best = f
				case f.Filed.Equal(best.Filed) && !f.Start.IsZero() && !best.Start.IsZero() && fDays < bestDays:
					// Same filing date: prefer shorter duration.
					best = f
				}
			}
		}

		if best != nil {
			return best.Val, true
		}
	}

	return 0, false
}

// ResolveLongestDuration is like ResolveDirect but prefers the longest
// duration fact, used for YTD cumulative per-share values to avoid summing
// individually rounded quarterly values.
func ResolveLongestDuration(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) (float64, bool) {
	return resolveLongestDurationBounded(cf, m, periodEnd, formType, 0)
}

// resolveLongestDurationBounded backs ResolveLongestDuration. minDays
// requires a 10-Q fact's duration to exceed that bound; callers needing
// "truly YTD" values pass minDays > ytdThresholdDays so single-quarter
// facts aren't mistaken for multi-quarter cumulatives.
func resolveLongestDurationBounded(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string, minDays int) (float64, bool) {
	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	normalPeriodEnd := NormalizeEventDate(periodEnd, formType)

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		var best *Fact

		for i := range facts {
			f := &facts[i]

			if !NormalizeEventDate(f.End, formType).Equal(normalPeriodEnd) {
				continue
			}

			if f.Form != formType {
				continue
			}

			// Only duration concepts have cumulative variants.
			if f.Start.IsZero() {
				continue
			}

			days := f.End.Sub(f.Start).Hours() / 24
			if formType == "10-K" {
				if days < 300 {
					continue
				}
			} else {
				// 10-Q cumulative values span the fiscal YTD (<= ~274 days at
				// Q3 for a calendar fiscal year). AMZN files a trailing
				// 12-month (365-day) fact at Q3 alongside the 3-month and
				// YTD facts — that TTM value would be picked as "longest"
				// and then used as the "cumulative Q3" for Q4 synthesis,
				// producing wildly wrong Q4 values. Cap at 300 days to
				// exclude TTM while still accepting YTD durations.
				if days > 300 {
					continue
				}

				if minDays > 0 && int(days) < minDays {
					continue
				}
			}

			if best == nil || f.Filed.After(best.Filed) {
				best = f
			} else if f.Filed.Equal(best.Filed) && !best.Start.IsZero() {
				fDays := f.End.Sub(f.Start).Hours() / 24
				bestDays := best.End.Sub(best.Start).Hours() / 24

				if fDays > bestDays {
					best = f
				}
			}
		}

		if best != nil {
			return best.Val, true
		}
	}

	return 0, false
}

// ytdThresholdDays is the maximum duration (in days) of a single-quarter fact.
// Facts longer than this in a 10-Q filing are treated as YTD cumulative values
// that need de-cumulation. A standard quarter is ~90 days; 120 provides margin
// for unusual fiscal calendars while cleanly excluding 2-quarter YTD (~180 days).
const ytdThresholdDays = 120

// needsDecumulation reports whether a flow field's best-matching 10-Q fact
// is a YTD cumulative value (no single-quarter fact is available).
func needsDecumulation(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) bool {
	if formType != "10-Q" {
		return false
	}

	if m.StatementType != StmtFlow {
		return false
	}

	normalPeriodEnd := NormalizeEventDate(periodEnd, formType)

	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		var hasMatch bool

		for i := range facts {
			f := &facts[i]

			if f.Form != formType {
				continue
			}

			if !NormalizeEventDate(f.End, formType).Equal(normalPeriodEnd) {
				continue
			}

			if f.Start.IsZero() {
				continue // instant concept, not applicable
			}

			hasMatch = true

			days := f.End.Sub(f.Start).Hours() / 24
			if days <= ytdThresholdDays {
				return false // found a quarterly fact, no de-cumulation needed
			}
		}

		if hasMatch {
			return true // this tag has only YTD facts
		}
	}

	return false
}

// hasNonQuarterlyDPSDeclarationCadence reports whether the company tags
// CommonStockDividendsPerShareDeclared with non-quarterly cadence (a non-Q1
// 10-Q where single-period equals YTD). Filers like LLY declare semi-annually
// but pay quarterly; they need cash-paid DPS for Sharadar parity.
func hasNonQuarterlyDPSDeclarationCadence(cf *CompanyFacts) bool {
	facts, ok := cf.Facts["CommonStockDividendsPerShareDeclared"]
	if !ok {
		return false
	}

	type periodKey struct {
		end time.Time
	}

	type periodFacts struct {
		single float64
		ytd    float64
		hasS   bool
		hasY   bool
	}

	groups := make(map[periodKey]*periodFacts)

	for i := range facts {
		f := &facts[i]
		if f.Form != "10-Q" || f.End.IsZero() || f.Start.IsZero() {
			continue
		}

		// Only consider facts from the last 3 years to avoid stale anomalies.
		if time.Since(f.End) > 3*365*24*time.Hour {
			continue
		}

		days := f.End.Sub(f.Start).Hours() / 24
		key := periodKey{end: f.End}

		pf, ok := groups[key]
		if !ok {
			pf = &periodFacts{}
			groups[key] = pf
		}

		// Single quarter ≈ 90 days; YTD H1+ ≥ 150 days.
		switch {
		case days <= 100:
			pf.single = f.Val
			pf.hasS = true
		case days >= 150:
			pf.ytd = f.Val
			pf.hasY = true
		}
	}

	for _, pf := range groups {
		if pf.hasS && pf.hasY && pf.single == pf.ytd && pf.single > 0 {
			return true
		}
	}

	return false
}

// conceptFiledQuarterly reports whether any of tags has a 10-Q fact within
// the last 3 years. The recency check avoids false positives from discontinued
// tags (e.g. AAPL's AccruedIncomeTaxesCurrent, last on 10-Q in 2010).
func conceptFiledQuarterly(cf *CompanyFacts, tags []string) bool {
	return conceptFiledOnForm(cf, tags, "10-Q")
}

// conceptFiledAnnually is the 10-K counterpart of conceptFiledQuarterly.
func conceptFiledAnnually(cf *CompanyFacts, tags []string) bool {
	return conceptFiledOnForm(cf, tags, "10-K")
}

func conceptFiledOnForm(cf *CompanyFacts, tags []string, form string) bool {
	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		for i := range facts {
			if facts[i].Form == form && !facts[i].End.IsZero() {
				// Consider only facts from the last ~3 years
				age := time.Since(facts[i].End)
				if age < 3*365*24*time.Hour {
					return true
				}
			}
		}
	}

	return false
}

// ResolveAllFields resolves all configured field mappings for a given period.
// Direct fields are resolved first, then derived fields are computed from the
// resolved values.
func ResolveAllFields(cf *CompanyFacts, periodEnd time.Time, formType string) map[string]float64 {
	resolved := make(map[string]float64)

	for _, m := range FieldMappings {
		// RequireQuarterly skips fields whose tags aren't filed on 10-Q by
		// this company (so we only include balance-sheet lines, not annual
		// note disclosures).
		if m.RequireQuarterly && !conceptFiledQuarterly(cf, m.XBRLTags) {
			continue
		}

		// ExcludeIfQuarterly skips fields when a sentinel concept signals the
		// field's tags are sub-components of a broader line. ExcludeIfQuarterlyUnless
		// cancels the skip when one of its concepts is also filed quarterly.
		if len(m.ExcludeIfQuarterly) > 0 && conceptFiledQuarterly(cf, m.ExcludeIfQuarterly) &&
			(len(m.ExcludeIfQuarterlyUnless) <= 0 || !conceptFiledQuarterly(cf, m.ExcludeIfQuarterlyUnless)) {
			continue
		}

		// ExcludeIfAnnual skips when a sentinel concept is filed on 10-K
		// (gates the MCD-style op-lease-current operand of debt_current).
		if len(m.ExcludeIfAnnual) > 0 && conceptFiledAnnually(cf, m.ExcludeIfAnnual) {
			continue
		}

		// RequireIfQuarterly: only resolve when a sentinel concept is
		// filed on 10-Q. The inverse of ExcludeIfQuarterly.
		if len(m.RequireIfQuarterly) > 0 && !conceptFiledQuarterly(cf, m.RequireIfQuarterly) {
			continue
		}

		switch m.Type {
		case MappingDirect:
			if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
				if m.Negate {
					val = -val
				}

				resolved[m.FieldName] = val
			}

		case MappingDerived:
			// Try direct fallback tags first. FallbackRequireIfQuarterly
			// gates the attempt so non-standard filers (BRK/B without COGS)
			// skip the fallback and use the formula.
			useFallback := len(m.FallbackTags) > 0
			if useFallback && len(m.FallbackRequireIfQuarterly) > 0 {
				useFallback = conceptFiledQuarterly(cf, m.FallbackRequireIfQuarterly)
			}

			if useFallback && len(m.FallbackExcludeIfQuarterly) > 0 && conceptFiledQuarterly(cf, m.FallbackExcludeIfQuarterly) {
				useFallback = false
			}

			if useFallback {
				if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
					if m.Negate {
						val = -val
					}

					// Prefer the formula when the fallback resolves to zero
					// but a derived value is non-zero (e.g. MCD tags
					// DebtCurrent=0 on 10-K while sub-components carry real
					// values that Sharadar rolls into debt_current).
					if m.PreferFormulaWhenFallbackZero && val == 0 {
						if fv, fok := computeDerived(m, resolved); fok && fv != 0 {
							resolved[m.FieldName] = fv

							continue
						}
					}

					resolved[m.FieldName] = val

					continue
				}
			}

			// Compute from formula
			if val, ok := computeDerived(m, resolved); ok {
				resolved[m.FieldName] = val
			}
		}
	}

	// Health insurers (UNH) report medical claims reserves under
	// LiabilityForClaimsAndClaimsAdjustmentExpense, which Sharadar includes
	// in payables. BRK-style insurance conglomerates don't file
	// PolicyholderBenefitsAndClaimsIncurredNet so this gate skips them.
	if conceptFiledQuarterly(cf, []string{"PolicyholderBenefitsAndClaimsIncurredNet"}) {
		if v, ok := ResolveDirect(cf, FieldMapping{
			XBRLTags:      []string{"LiabilityForClaimsAndClaimsAdjustmentExpense"},
			StatementType: StmtPointInTime,
		}, periodEnd, formType); ok {
			resolved["Payables"] += v
		}
	}

	overrideForEmbeddedCurrentDebt(cf, resolved, periodEnd, formType)
	overrideSGAForSmallOtherSGA(cf, resolved, periodEnd, formType)
	overrideIntangiblesForSplitComponents(cf, resolved, periodEnd, formType)
	overridePreferredDividendsForMezzanineAccretion(cf, resolved)
	overrideRandDExpensesForFootnoteOnly(resolved)
	overrideRandDForEnergyFilers(cf, resolved, periodEnd, formType)
	overrideCashAndEquivalentsIncludeRestrictedCash(cf, resolved, periodEnd, formType)
	overrideDebtCurrentForSmallAmortization(cf, resolved, periodEnd, formType)
	overrideBalanceSheetForIndustrialFinancialFiler(cf, resolved, periodEnd, formType)

	return resolved
}

// isIndustrialFinancialFiler detects CAT-style industrial manufacturers with a
// captive financial-services subsidiary, via the combination of CostOfRevenue,
// OtherOperatingIncomeExpenseNet, and CostsAndExpenses filed quarterly. The
// ExplorationExpense exclusion keeps XOM on its existing deriver.
func isIndustrialFinancialFiler(cf *CompanyFacts) bool {
	if !conceptFiledQuarterly(cf, []string{"CostOfRevenue"}) {
		return false
	}

	if !conceptFiledQuarterly(cf, []string{"OtherOperatingIncomeExpenseNet"}) {
		return false
	}

	if !conceptFiledQuarterly(cf, []string{"CostsAndExpenses"}) {
		return false
	}

	if conceptFiledQuarterly(cf, []string{"ExplorationExpense"}) {
		return false
	}

	return true
}

// overrideBalanceSheetForIndustrialFinancialFiler reclassifies CAT-style
// balance-sheet line items to match Sharadar's treatment of captive-finance
// assets and contract-liability deposits.
func overrideBalanceSheetForIndustrialFinancialFiler(cf *CompanyFacts, fields map[string]float64, periodEnd time.Time, formType string) {
	if !isIndustrialFinancialFiler(cf) {
		return
	}

	arC, _ := resolveInstantValue(cf, "AccountsReceivableNetCurrent", periodEnd, formType)
	arNC, _ := resolveInstantValue(cf, "AccountsReceivableNetNoncurrent", periodEnd, formType)
	notesC, _ := resolveInstantValue(cf, "NotesAndLoansReceivableNetCurrent", periodEnd, formType)
	notesNC, _ := resolveInstantValue(cf, "NotesAndLoansReceivableNetNoncurrent", periodEnd, formType)

	totalRecv := arC + arNC + notesC + notesNC
	if totalRecv > 0 {
		fields["Receivables"] = totalRecv
	}

	fields["Investments"] = 0
	fields["InvestmentsCurrent"] = 0
	fields["InvestmentsNonCurrent"] = 0

	// Reclassify contract-liability deposits. Industrial OEMs collect customer
	// advance payments for long-lead machinery orders under
	// ContractWithCustomerLiabilityCurrent; Sharadar reports these as deposits,
	// not deferred_revenue.
	if contractLiab, ok := resolveInstantValue(cf, "ContractWithCustomerLiabilityCurrent", periodEnd, formType); ok && contractLiab > 0 {
		fields["Deposits"] = contractLiab
		fields["DeferredRevenue"] = 0
	}

	// CAT's NoncurrentDeferredAndRefundableIncomeTaxes (an extension concept)
	// is Sharadar's tax_assets source. Falling back to DeferredTaxAssetsNet
	// (10-K only) covers older periods. Both are net DTA-DTL, so zero out
	// TaxLiabilities to avoid double-counting the 10-K-only footnote.
	if dta, ok := resolveInstantValue(cf, "NoncurrentDeferredAndRefundableIncomeTaxes", periodEnd, formType); ok && dta > 0 {
		fields["TaxAssets"] = dta
		fields["TaxLiabilities"] = 0
	} else if dta, ok := resolveInstantValue(cf, "DeferredTaxAssetsNet", periodEnd, formType); ok && dta > 0 {
		fields["TaxAssets"] = dta
		fields["TaxLiabilities"] = 0
	}

	// Sharadar reports share_based_compensation = 0 for CAT across all
	// quarters. CAT tags both ShareBasedCompensation and
	// AllocatedShareBasedCompensationExpense inconsistently (Q1 standalone
	// only for ShareBasedCompensation), so Q4 synthesis from Annual − Q1−Q3
	// yields -44M from an incomplete fact sequence. Zeroing matches Sharadar.
	fields["ShareBasedCompensation"] = 0
}

// overrideDebtCurrentForSmallAmortization zeros LongTermDebtCurrent and rolls
// it into non-current when it's under 2% of total long-term debt and no
// other current-debt components are filed (matches Sharadar's treatment of
// term-loan minimum amortization).
func overrideDebtCurrentForSmallAmortization(cf *CompanyFacts, resolved map[string]float64, periodEnd time.Time, formType string) {
	ltdCurrent, hasLtdCurrent := resolveInstantValue(cf, "LongTermDebtCurrent", periodEnd, formType)
	if !hasLtdCurrent || ltdCurrent <= 0 {
		return
	}

	ltdNC, hasLtdNC := resolveInstantValue(cf, "LongTermDebtNoncurrent", periodEnd, formType)
	if !hasLtdNC || ltdNC <= 0 {
		return
	}

	totalLtd := ltdCurrent + ltdNC
	if ltdCurrent/totalLtd >= 0.02 {
		return
	}

	// Skip when any other current-debt component is filed; that signals a
	// richer current-debt balance-sheet section Sharadar would include.
	for _, tag := range []string{
		"ShortTermBorrowings",
		"CommercialPaper",
		"OperatingLeaseLiabilityCurrent",
		"FinanceLeaseLiabilityCurrent",
	} {
		if v, ok := resolveInstantValue(cf, tag, periodEnd, formType); ok && v > 0 {
			return
		}
	}

	resolved["LongTermDebtCurrentMaturities"] = 0
	resolved["DebtCurrent"] -= ltdCurrent
	resolved["TotalDebt"] -= ltdCurrent

	if _, ok := resolved["InvestedCapital"]; ok {
		resolved["InvestedCapital"] -= ltdCurrent
	}
}

// overrideCashAndEquivalentsIncludeRestrictedCash adds restricted cash to
// cash_and_equivalents when the filer reports it as a separate balance-sheet
// item, matching Sharadar's cashneq treatment.
func overrideCashAndEquivalentsIncludeRestrictedCash(cf *CompanyFacts, resolved map[string]float64, periodEnd time.Time, formType string) {
	restricted, hasRestricted := resolveInstantValue(cf, "RestrictedCash", periodEnd, formType)
	if !hasRestricted || restricted <= 0 {
		// XOM-style filers use the -AtCarryingValue suffix instead.
		restricted, hasRestricted = resolveInstantValue(cf, "RestrictedCashAndCashEquivalentsAtCarryingValue", periodEnd, formType)
		if !hasRestricted || restricted <= 0 {
			return
		}
	}

	combined, hasCombined := resolveInstantValue(cf, "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", periodEnd, formType)
	if !hasCombined || combined <= 0 {
		return
	}

	current := resolved["CashAndEquivalents"]
	if combined <= current {
		return
	}

	delta := combined - current
	resolved["CashAndEquivalents"] = combined

	if _, ok := resolved["InvestedCapital"]; ok {
		resolved["InvestedCapital"] -= delta
	}
}

// overrideRandDExpensesForFootnoteOnly zeros RandDExpenses when it's under
// 1% of revenue and under $10M absolute (heuristic for footnote-only R&D,
// which Sharadar excludes).
func overrideRandDExpensesForFootnoteOnly(resolved map[string]float64) {
	rd, ok := resolved["RandDExpenses"]
	if !ok || rd == 0 {
		return
	}

	revenue := resolved["Revenues"]
	if revenue <= 0 {
		return
	}

	if rd >= 10_000_000 {
		return
	}

	if rd/revenue >= 0.01 {
		return
	}

	resolved["RandDExpenses"] = 0
}

// overrideRandDForEnergyFilers replaces RandDExpenses with ExplorationExpense
// for energy filers (XOM-style). Sharadar maps the "Exploration expenses"
// line to r_and_d_expenses for the energy sector. Gate: ExplorationExpense
// is filed quarterly.
func overrideRandDForEnergyFilers(cf *CompanyFacts, resolved map[string]float64, periodEnd time.Time, formType string) {
	if !conceptFiledQuarterly(cf, []string{"ExplorationExpense"}) {
		return
	}

	val, ok := ResolveDirect(cf, FieldMapping{
		XBRLTags:      []string{"ExplorationExpense"},
		StatementType: StmtFlow,
	}, periodEnd, formType)
	if !ok {
		resolved["RandDExpenses"] = 0

		return
	}

	resolved["RandDExpenses"] = val
}

// overridePreferredDividendsForMezzanineAccretion reclassifies the residual
// between NetIncome and NetIncomeCommonStock as preferred_dividends when the
// filer reports explicit preferred dividends but no NCI tag. Sharadar treats
// this residual as a single preferred_dividends_income_statement_impact and
// zeros out NCI.
func overridePreferredDividendsForMezzanineAccretion(cf *CompanyFacts, resolved map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{
		"PreferredStockDividendsIncomeStatementImpact",
		"PreferredStockDividendsAndOtherAdjustments",
	}) {
		return
	}

	if conceptFiledQuarterly(cf, []string{"NetIncomeLossAttributableToNoncontrollingInterest"}) {
		return
	}

	ci := resolved["ConsolidatedIncome"]
	nic := resolved["NetIncomeCommonStock"]

	if ci == 0 || nic == 0 {
		return
	}

	totalDeduction := ci - nic
	if totalDeduction <= 0 {
		return
	}

	explicitPref := resolved["PreferredDividendsIncomeStatementImpact"]
	if explicitPref >= totalDeduction-1.0 {
		return
	}

	resolved["PreferredDividendsIncomeStatementImpact"] = totalDeduction
	resolved["NetIncomeToNonControllingInterests"] = 0
}

// overrideIntangiblesForSplitComponents recomputes Intangibles from component
// tags (Goodwill + IntangibleAssetsNetExcludingGoodwill, or finite/indefinite
// splits) when the component sum exceeds the default. The override never
// shrinks Intangibles and only counts tags filed on a 10-Q.
func overrideIntangiblesForSplitComponents(cf *CompanyFacts, resolved map[string]float64, periodEnd time.Time, formType string) {
	// GS-pattern filer: contract liabilities are bundled under Other assets,
	// intangibles are not a separate balance-sheet line.
	if conceptFiledQuarterly(cf, []string{"CustomerAndOtherPayables"}) {
		return
	}

	gw, _ := resolveInstantValue(cf, "Goodwill", periodEnd, formType)
	if gw == 0 {
		return
	}

	// AMZN-style filers disclose ex-goodwill intangibles only in 10-K
	// footnotes and Sharadar excludes them. If neither the rollup nor any
	// component tag is filed on 10-Q, leave Intangibles as-is (Goodwill-only
	// after the default formula).
	INegFiledQuarterly := conceptFiledQuarterly(cf, []string{"IntangibleAssetsNetExcludingGoodwill"})
	finiteFiledQuarterly := conceptFiledQuarterly(cf, []string{"FiniteLivedIntangibleAssetsNet"})
	indefFiledQuarterly := conceptFiledQuarterly(cf, []string{"IndefiniteLivedIntangibleAssetsExcludingGoodwill"})
	indefTMFiledQuarterly := conceptFiledQuarterly(cf, []string{"IndefiniteLivedTrademarks"})
	otherIAFiledQuarterly := conceptFiledQuarterly(cf, []string{"OtherIntangibleAssetsNet"})

	if !INegFiledQuarterly && !finiteFiledQuarterly && !indefFiledQuarterly &&
		!indefTMFiledQuarterly && !otherIAFiledQuarterly {
		return
	}

	var exGoodwill float64

	// Prefer the rollup tag when it has a value for THIS period (captures all
	// ex-goodwill intangibles without double-counting components). Otherwise
	// sum whichever components the filer reports on 10-Q — CELH Q2/Q3 2025
	// stopped filing the rollup after its Alani acquisition and reports
	// FiniteLived + IndefiniteLived as separate lines.
	if INegFiledQuarterly {
		if INeg, _ := resolveInstantValue(cf, "IntangibleAssetsNetExcludingGoodwill", periodEnd, formType); INeg > 0 {
			exGoodwill = INeg
		}
	}

	if exGoodwill == 0 {
		if finiteFiledQuarterly {
			if v, ok := resolveInstantValue(cf, "FiniteLivedIntangibleAssetsNet", periodEnd, formType); ok {
				exGoodwill += v
			}
		}

		if indefFiledQuarterly {
			if v, ok := resolveInstantValue(cf, "IndefiniteLivedIntangibleAssetsExcludingGoodwill", periodEnd, formType); ok {
				exGoodwill += v
			}
		}

		if indefTMFiledQuarterly {
			if v, ok := resolveInstantValue(cf, "IndefiniteLivedTrademarks", periodEnd, formType); ok {
				exGoodwill += v
			}
		}

		// OtherIntangibleAssetsNet is an additional line only when the
		// ex-goodwill rollup is not the filer's standard (otherwise it's
		// already part of INeg). Mirror _otherIntangiblesAdditional.
		if !INegFiledQuarterly && otherIAFiledQuarterly {
			if v, ok := resolveInstantValue(cf, "OtherIntangibleAssetsNet", periodEnd, formType); ok {
				exGoodwill += v
			}
		}
	}

	computed := gw + exGoodwill
	if computed == 0 {
		return
	}

	current := resolved["Intangibles"]
	if computed <= current {
		return
	}

	delta := computed - current
	resolved["Intangibles"] = computed

	if tav, ok := resolved["TangibleAssetValue"]; ok {
		resolved["TangibleAssetValue"] = tav - delta
	}

	if ic, ok := resolved["InvestedCapital"]; ok {
		resolved["InvestedCapital"] = ic - delta
	}
}

// overrideSGAForSmallOtherSGA switches to the broader
// SellingGeneralAndAdministrativeExpense tag for product-sales filers (KO),
// gated to non-franchisors via FranchiseRevenue/FranchiseCosts so MCD-style
// franchisors keep the OtherSGA preference.
func overrideSGAForSmallOtherSGA(cf *CompanyFacts, resolved map[string]float64, periodEnd time.Time, formType string) {
	if _, ok := cf.Facts["FranchiseRevenue"]; ok {
		return
	}

	if _, ok := cf.Facts["FranchiseCosts"]; ok {
		return
	}

	sga, sgaOK := ResolveDirect(cf, FieldMapping{
		XBRLTags:      []string{"SellingGeneralAndAdministrativeExpense"},
		StatementType: StmtFlow,
	}, periodEnd, formType)
	if !sgaOK || sga <= 0 {
		return
	}

	resolved["SellingGeneralAndAdministrativeExpense"] = sga
}

// overrideForEmbeddedCurrentDebt rewrites DebtCurrent, DeferredRevenue, and
// TotalDebt when current debt is embedded in accrued expenses (identity:
// AccountsPayable + AccruedLiabilitiesCurrent + ContractLiabilityCurrent ~=
// LiabilitiesCurrent).
func overrideForEmbeddedCurrentDebt(cf *CompanyFacts, resolved map[string]float64, periodEnd time.Time, formType string) {
	// Both sentinels must be filed quarterly.
	if !conceptFiledQuarterly(cf, []string{"AccruedLiabilitiesCurrent"}) ||
		!conceptFiledQuarterly(cf, []string{"ContractWithCustomerLiabilityCurrent"}) {
		return
	}

	liabCurrent, ok := resolveInstantValue(cf, "LiabilitiesCurrent", periodEnd, formType)
	if !ok || liabCurrent == 0 {
		return
	}

	ap, _ := resolveInstantValue(cf, "AccountsPayableCurrent", periodEnd, formType)
	accrued, _ := resolveInstantValue(cf, "AccruedLiabilitiesCurrent", periodEnd, formType)
	contract, hasContract := resolveInstantValue(cf, "ContractWithCustomerLiabilityCurrent", periodEnd, formType)

	if !hasContract || contract == 0 {
		return
	}

	// Identity check within 0.1%: when it holds, current debt is embedded in
	// AccruedLiabilitiesCurrent. AMZN matches at 0%; NVDA sits near 1%, so a
	// tight tolerance keeps them distinct.
	sumExplicit := ap + accrued + contract
	if math.Abs(sumExplicit-liabCurrent)/liabCurrent > 0.001 {
		return
	}

	// Zero current debt; total_debt collapses to debt_non_current.
	oldTotalDebt := resolved["TotalDebt"]

	if _, ok := resolved["DebtCurrent"]; ok {
		resolved["DebtCurrent"] = 0
	}

	if dnc, ok := resolved["DebtNonCurrent"]; ok {
		resolved["TotalDebt"] = dnc
	}

	if ic, ok := resolved["InvestedCapital"]; ok {
		resolved["InvestedCapital"] = ic + (resolved["TotalDebt"] - oldTotalDebt)
	}

	// DeferredRevenue: the current contract liability is a separate line, so
	// Sharadar reports it even though AccruedLiabilitiesCurrent is also filed.
	// Non-current contract liability is rolled into OtherLiabilitiesNoncurrent
	// and excluded — current only.
	resolved["DeferredRevenue"] = contract
}

// resolveInstantValue returns the best-matching instant (balance-sheet) value
// for a us-gaap concept at the given period end and form type.
func resolveInstantValue(cf *CompanyFacts, concept string, periodEnd time.Time, formType string) (float64, bool) {
	return ResolveDirect(cf, FieldMapping{
		XBRLTags:      []string{concept},
		StatementType: StmtPointInTime,
	}, periodEnd, formType)
}

// recomputeWavgDerived re-evaluates the per-share metrics that depend on
// WeightedAverageShares (or WeightedAverageSharesDiluted) after the wavg
// field has been overwritten post-synthesis. Keeps the metric values
// consistent with the new denominator without rerunning the full derivation
// pipeline.
func recomputeWavgDerived(fields map[string]float64) {
	for _, fieldName := range []string{
		"FreeCashFlowPerShare",
		"BookValuePerShare",
		"SalesPerShare",
		"TangibleAssetsBookValuePerShare",
	} {
		for _, m := range FieldMappings {
			if m.FieldName != fieldName {
				continue
			}

			if v, ok := computeDerived(m, fields); ok {
				fields[m.FieldName] = v
			}

			break
		}
	}
}

// computeDerived evaluates a derived field's formula using already-resolved values.
func computeDerived(m FieldMapping, resolved map[string]float64) (float64, bool) {
	// When OptionalOperands is set, missing operands are treated as 0 and the
	// formula resolves if at least one operand is present. This handles fields
	// like Investments = InvestmentsCurrent + InvestmentsNonCurrent where a
	// company may report only one component, and InvestedCapital where
	// Intangibles may be absent (the company has none).
	if m.OptionalOperands && (m.Op == OpAdd || m.Op == OpLinearCombination) {
		sum := 0.0
		found := false

		for i, op := range m.Operands {
			if v, ok := resolved[op]; ok {
				if m.Op == OpLinearCombination {
					sum += m.Coefficients[i] * v
				} else {
					sum += v
				}

				found = true
			}
		}

		if found {
			return sum, true
		}

		return 0, false
	}

	// All operands must be present
	vals := make([]float64, len(m.Operands))
	for i, op := range m.Operands {
		v, ok := resolved[op]
		if !ok {
			return 0, false
		}

		vals[i] = v
	}

	switch m.Op {
	case OpAdd:
		sum := 0.0
		for _, v := range vals {
			sum += v
		}

		return sum, true

	case OpSubtract:
		if len(vals) < 2 {
			return 0, false
		}

		return vals[0] - vals[1], true

	case OpDivide:
		if len(vals) < 2 || vals[1] == 0 {
			return 0, false
		}

		result := vals[0] / vals[1]

		if m.RoundDigits > 0 {
			pow := math.Pow(10, float64(m.RoundDigits))
			result = math.Round(result*pow) / pow
		}

		return result, true

	case OpLinearCombination:
		if len(vals) != len(m.Coefficients) {
			return 0, false
		}

		sum := 0.0
		for i, v := range vals {
			sum += m.Coefficients[i] * v
		}

		return sum, true
	}

	return 0, false
}

// OverrideDPSFromCash sets DividendsPerBasicCommonShare = cashPaid / wavg
// shares (rounded to 2 decimals), matching Sharadar's methodology.
func OverrideDPSFromCash(cf *CompanyFacts, fields map[string]float64, isMR bool, periodEnd time.Time, isAnnualView bool) {
	cashPaid, hasCash := fields["_absDividendsPaid"]
	shares, hasShares := fields["WeightedAverageShares"]

	if !hasCash || !hasShares || shares <= 0 {
		return
	}

	// CAT-style industrial-financial filers: Sharadar uses the 10-K declared
	// per-share value as the AR annual DPS; quarterly cadence still uses
	// cash-paid.
	if !isMR && isAnnualView && isIndustrialFinancialFiler(cf) {
		return
	}

	// When the cash-paid value comes from PaymentsOfDividends (no
	// PaymentsOfDividendsCommonStock filed), gate the override on filer
	// cadence and dimension/period.
	if !conceptFiledQuarterly(cf, []string{"PaymentsOfDividendsCommonStock"}) {
		if !hasNonQuarterlyDPSDeclarationCadence(cf) {
			return
		}

		if !isMR && !hasQuarterlyDPSDeclarationAt(cf, periodEnd) {
			return
		}
	}

	// WMT-style annual-only declared DPS: Sharadar's quarterly value is
	// AnnualDeclared / 4 truncated to 3 decimals.
	if !isAnnualView && dpsIsAnnualOnly(cf) {
		if annualDps, ok := latestAnnualDpsDeclared(cf, periodEnd); ok && annualDps > 0 {
			truncated := math.Trunc(annualDps/4*1000) / 1000
			fields["DividendsPerBasicCommonShare"] = truncated

			return
		}
	}

	dps := cashPaid / shares
	// Round to 2 decimal places.
	dps = math.Round(dps*100) / 100
	fields["DividendsPerBasicCommonShare"] = dps
}

// dpsIsAnnualOnly reports true when CommonStockDividendsPerShareDeclared
// facts always carry the full-year amount (WMT-style: even "quarterly"
// duration contexts hold the annualized value).
func dpsIsAnnualOnly(cf *CompanyFacts) bool {
	facts, ok := cf.Facts["CommonStockDividendsPerShareDeclared"]
	if !ok {
		return false
	}

	var (
		annual       float64
		hasAnnual    bool
		maxShortVal  float64
		hasShortFact bool
	)

	for i := range facts {
		f := &facts[i]
		if f.Form != "10-Q" && f.Form != "10-K" {
			continue
		}

		if f.Start.IsZero() || f.End.IsZero() {
			continue
		}

		days := f.End.Sub(f.Start).Hours() / 24
		if days <= 0 {
			continue
		}

		if days >= 300 && days <= 400 {
			if f.Val > annual {
				annual = f.Val
			}

			hasAnnual = true

			continue
		}

		if days <= ytdThresholdDays {
			hasShortFact = true

			if f.Val > maxShortVal {
				maxShortVal = f.Val
			}
		}
	}

	if !hasAnnual {
		return false
	}

	// WMT-style: the max "quarterly-looking" declared value equals the
	// annual amount (the Q1 10-Q tags the annual-declared value on a
	// Q1 duration). AAPL-style: max quarterly value < annual amount.
	if !hasShortFact {
		return true
	}

	return maxShortVal >= annual-0.0001
}

// latestAnnualDpsDeclared returns the max declared DPS whose [start, end]
// window contains periodEnd. The max handles "stub 0" facts for quarters
// without a new declaration.
func latestAnnualDpsDeclared(cf *CompanyFacts, periodEnd time.Time) (float64, bool) {
	facts, ok := cf.Facts["CommonStockDividendsPerShareDeclared"]
	if !ok {
		return 0, false
	}

	var (
		bestVal float64
		found   bool
	)

	for i := range facts {
		f := &facts[i]
		if f.Form != "10-Q" && f.Form != "10-K" {
			continue
		}

		if f.Start.IsZero() || f.End.IsZero() {
			continue
		}

		if f.Start.After(periodEnd) || f.End.Before(periodEnd) {
			continue
		}

		if !found || f.Val > bestVal {
			bestVal = f.Val
			found = true
		}
	}

	return bestVal, found
}

// hasQuarterlyDPSDeclarationAt reports whether a 10-Q DPS-declared fact
// exists at periodEnd. Distinguishes in-year quarters from fiscal year-end
// for non-quarterly declarers (LLY): Sharadar uses XBRL-declared values for
// year-end and cash-paid for in-year quarters in that case.
func hasQuarterlyDPSDeclarationAt(cf *CompanyFacts, periodEnd time.Time) bool {
	facts, ok := cf.Facts["CommonStockDividendsPerShareDeclared"]
	if !ok {
		return false
	}

	for i := range facts {
		f := &facts[i]
		if f.Form == "10-Q" && f.End.Equal(periodEnd) {
			return true
		}
	}

	return false
}

// resolveSharesBasicAsOf returns the shares outstanding value from the most
// recently filed 10-K or 10-Q as of the given date. Returns (0, false) when
// no suitable fact is found.
func resolveSharesBasicAsOf(cf *CompanyFacts, asOfDate time.Time) (float64, bool) {
	staleThreshold := asOfDate.AddDate(-2, 0, 0)

	var (
		bestConcept string
		bestFiled   time.Time
		found       bool
	)

	for _, conceptName := range []string{
		"EntityCommonStockSharesOutstanding",
		"CommonStockSharesOutstanding",
	} {
		facts, ok := cf.Facts[conceptName]
		if !ok {
			continue
		}

		for i := range facts {
			f := &facts[i]

			if f.Form != "10-K" && f.Form != "10-Q" {
				continue
			}

			if f.Filed.After(asOfDate) {
				continue
			}

			if f.Filed.Before(staleThreshold) {
				continue
			}

			if !found || f.Filed.After(bestFiled) {
				bestConcept = conceptName
				bestFiled = f.Filed
				found = true
			}
		}
	}

	if !found {
		return 0, false
	}

	// Sum all facts from the same concept with the same filing date. For a
	// single-class filer this is a single fact; for a multi-class filer the
	// filing reports one fact per class (Class A + Class B), and Sharadar's
	// shares_basic matches the sum of raw counts across classes.
	total := 0.0

	for i := range cf.Facts[bestConcept] {
		f := &cf.Facts[bestConcept][i]
		if f.Filed.Equal(bestFiled) && (f.Form == "10-K" || f.Form == "10-Q") {
			total += f.Val
		}
	}

	return total, true
}

// resolveSharesBasicForAnnualAR returns the as-reported shares outstanding
// for an annual (10-K) period. Tries the post-period cover-page DEI first,
// then fiscal-Q2 DEI, then the generic latest-filed resolution.
func resolveSharesBasicForAnnualAR(cf *CompanyFacts, arFiledDate, periodEnd time.Time) (float64, bool) {
	staleThreshold := arFiledDate.AddDate(-2, 0, 0)
	postPeriodEndWindow := periodEnd.AddDate(0, 4, 0) // ~4 months after FY end

	// Convention #1: latest-filed DEI whose end is after fiscal year end
	// (cover-page "as of recent date" disclosure), gated to within 4 months.
	var (
		postPeriodConcept string
		postPeriodFiled   time.Time
		postPeriodFound   bool
	)

	for _, conceptName := range []string{
		"EntityCommonStockSharesOutstanding",
		"CommonStockSharesOutstanding",
	} {
		facts, ok := cf.Facts[conceptName]
		if !ok {
			continue
		}

		for i := range facts {
			f := &facts[i]

			if f.Form != "10-K" && f.Form != "10-Q" {
				continue
			}

			if f.Filed.After(arFiledDate) || f.Filed.Before(staleThreshold) {
				continue
			}

			if !f.End.After(periodEnd) || f.End.After(postPeriodEndWindow) {
				continue
			}

			if !postPeriodFound || f.Filed.After(postPeriodFiled) {
				postPeriodConcept = conceptName
				postPeriodFiled = f.Filed
				postPeriodFound = true
			}
		}
	}

	if postPeriodFound {
		total := 0.0

		for i := range cf.Facts[postPeriodConcept] {
			f := &cf.Facts[postPeriodConcept][i]
			if f.Filed.Equal(postPeriodFiled) && (f.Form == "10-K" || f.Form == "10-Q") && f.End.After(periodEnd) && !f.End.After(postPeriodEndWindow) {
				total += f.Val
			}
		}

		if total > 0 {
			return total, true
		}
	}

	// Convention #2: fall back to the DEI at fiscal Q2 end (period end − 6 months).
	q2End := periodEnd.AddDate(0, -6, 0)
	q2Window := 30 * 24 * time.Hour

	var (
		q2Concept string
		q2Filed   time.Time
		q2Found   bool
	)

	for _, conceptName := range []string{
		"EntityCommonStockSharesOutstanding",
		"CommonStockSharesOutstanding",
	} {
		facts, ok := cf.Facts[conceptName]
		if !ok {
			continue
		}

		for i := range facts {
			f := &facts[i]

			if f.Form != "10-K" && f.Form != "10-Q" {
				continue
			}

			if f.Filed.After(arFiledDate) || f.Filed.Before(staleThreshold) {
				continue
			}

			delta := f.End.Sub(q2End)
			if delta < 0 {
				delta = -delta
			}

			if delta > q2Window {
				continue
			}

			if !q2Found || f.Filed.After(q2Filed) {
				q2Concept = conceptName
				q2Filed = f.Filed
				q2Found = true
			}
		}
	}

	if q2Found {
		total := 0.0

		for i := range cf.Facts[q2Concept] {
			f := &cf.Facts[q2Concept][i]
			if f.Filed.Equal(q2Filed) && (f.Form == "10-K" || f.Form == "10-Q") {
				delta := f.End.Sub(q2End)
				if delta < 0 {
					delta = -delta
				}

				if delta <= q2Window {
					total += f.Val
				}
			}
		}

		if total > 0 {
			return total, true
		}
	}

	// Last resort: fall back to the generic latest-filed resolution.
	return resolveSharesBasicAsOf(cf, arFiledDate)
}

// resolveSharesBasicForMR returns the MR shares outstanding. When the latest
// 10-K DEI is actually the balance-sheet disclosure (not the cover-page
// recent-date count), falls back to the annual-AR resolver since Sharadar's
// MR uses the cover-page value.
func resolveSharesBasicForMR(cf *CompanyFacts, asOfDate time.Time) (float64, bool) {
	staleThreshold := asOfDate.AddDate(-2, 0, 0)

	var latestFact *Fact

	var latestConcept string

	for _, conceptName := range []string{
		"EntityCommonStockSharesOutstanding",
		"CommonStockSharesOutstanding",
	} {
		facts, ok := cf.Facts[conceptName]
		if !ok {
			continue
		}

		for i := range facts {
			f := &facts[i]

			if f.Form != "10-K" && f.Form != "10-Q" {
				continue
			}

			if f.Filed.After(asOfDate) || f.Filed.Before(staleThreshold) {
				continue
			}

			if latestFact == nil || f.Filed.After(latestFact.Filed) {
				latestFact = f
				latestConcept = conceptName
			}
		}
	}

	if latestFact == nil {
		return 0, false
	}

	// Balance-sheet DEI check: a companion Assets fact with the same filed
	// and end dates means this DEI is the financial-statement disclosure.
	if latestFact.Form == "10-K" && isBalanceSheetDEI(cf, latestFact) {
		if val, ok := resolveSharesBasicForAnnualAR(cf, latestFact.Filed, latestFact.End); ok {
			return val, true
		}
	}

	// Sum facts for multi-class filers (same concept + same filed date).
	total := 0.0

	for i := range cf.Facts[latestConcept] {
		f := &cf.Facts[latestConcept][i]
		if f.Filed.Equal(latestFact.Filed) && (f.Form == "10-K" || f.Form == "10-Q") {
			total += f.Val
		}
	}

	return total, true
}

// isBalanceSheetDEI reports whether a companion balance-sheet fact shares the
// DEI's filed and end dates. Cover-page DEIs (end dated weeks after period
// end) have no matching companion.
func isBalanceSheetDEI(cf *CompanyFacts, dei *Fact) bool {
	for _, concept := range []string{"Assets", "StockholdersEquity", "Liabilities"} {
		facts, ok := cf.Facts[concept]
		if !ok {
			continue
		}

		for i := range facts {
			f := &facts[i]
			if f.Filed.Equal(dei.Filed) && f.End.Equal(dei.End) {
				return true
			}
		}
	}

	return false
}

// resolveClassSharesAsOf returns Class A and Class B raw cover-page share
// counts from the most recent filing on or before asOfDate. Returns
// (filed, classA, classB, true) when both class values are available.
func resolveClassSharesAsOf(cf *CompanyFacts, asOfDate time.Time) (filed time.Time, classA, classB float64, ok bool) {
	type filingKey struct {
		filed time.Time
		form  string
	}

	// Prefer EntityCommonStockSharesOutstanding per class; fall back to
	// CommonStockSharesOutstanding only when Entity data is absent.
	type classPair struct {
		A, B        float64
		AFromEntity bool
		BFromEntity bool
	}

	pairs := make(map[filingKey]classPair)

	for _, csf := range cf.ClassShares {
		if csf.Form != "10-K" && csf.Form != "10-Q" {
			continue
		}

		if csf.Filed.After(asOfDate) {
			continue
		}

		k := filingKey{filed: csf.Filed, form: csf.Form}
		p := pairs[k]

		isEntity := csf.Concept == "EntityCommonStockSharesOutstanding"

		switch csf.Class {
		case "A":
			if isEntity || !p.AFromEntity {
				p.A = csf.Val
				p.AFromEntity = isEntity
			}
		case "B":
			if isEntity || !p.BFromEntity {
				p.B = csf.Val
				p.BFromEntity = isEntity
			}
		}

		pairs[k] = p
	}

	for k, p := range pairs {
		if p.A <= 0 || p.B <= 0 {
			continue
		}

		if !ok || k.filed.After(filed) {
			filed = k.filed
			classA = p.A
			classB = p.B
			ok = true
		}
	}

	return
}

// applyMRComparativeFilter zeroes MR quarterly flow fields whose concepts
// have no fact for periodEnd filed in a Y+1 filing. Below the 11-month
// threshold, no Y+1 filing has landed and the original fact is authoritative.
func applyMRComparativeFilter(cf *CompanyFacts, fields map[string]float64, periodEnd time.Time) {
	cutoff := periodEnd.AddDate(0, 11, 0)
	hasSubsequent := false

	for _, facts := range cf.Facts {
		for i := range facts {
			if facts[i].Filed.After(cutoff) {
				hasSubsequent = true

				break
			}
		}

		if hasSubsequent {
			break
		}
	}

	if !hasSubsequent {
		return
	}

	// Build field-by-name lookup for operand resolution.
	byName := make(map[string]*FieldMapping, len(FieldMappings))
	for i := range FieldMappings {
		byName[FieldMappings[i].FieldName] = &FieldMappings[i]
	}

	// collectLeafTags walks a derived field's operand tree and collects every
	// underlying XBRL tag so the filter treats a derived sum as having
	// comparatives whenever any operand's tag was re-reported.
	var collectLeafTags func(fieldName string, visited map[string]bool) []string

	collectLeafTags = func(fieldName string, visited map[string]bool) []string {
		if visited[fieldName] {
			return nil
		}

		visited[fieldName] = true

		fm, ok := byName[fieldName]
		if !ok {
			return nil
		}

		tags := append([]string(nil), fm.XBRLTags...)
		tags = append(tags, fm.FallbackTags...)

		for _, op := range fm.Operands {
			tags = append(tags, collectLeafTags(op, visited)...)
		}

		return tags
	}

	for _, m := range FieldMappings {
		if m.StatementType != StmtFlow || m.ValueType != "int64" {
			continue
		}

		if _, ok := fields[m.FieldName]; !ok {
			continue
		}

		tags := collectLeafTags(m.FieldName, make(map[string]bool))
		if len(tags) == 0 {
			continue
		}

		hasComparative := false

		for _, tag := range tags {
			if tag == "" {
				continue
			}

			for i := range cf.Facts[tag] {
				f := &cf.Facts[tag][i]
				if f.End.Equal(periodEnd) && f.Filed.After(cutoff) {
					hasComparative = true

					break
				}
			}

			if hasComparative {
				break
			}
		}

		if !hasComparative {
			fields[m.FieldName] = 0
		}
	}
}

// overrideNCFDebtResidual recomputes NetCashFlowDebt as financing - common -
// dividend, capturing items included in the financing total but not
// separately tagged.
func overrideNCFDebtResidual(cf *CompanyFacts, fields map[string]float64, periodEnd time.Time, formType string) {
	// Banks lack AssetsCurrent and have Deposits; insurance conglomerates
	// (BRK/B) lack AssetsCurrent without Deposits.
	lacksAssetsCurrent := !conceptFiledQuarterly(cf, []string{"AssetsCurrent"})
	isBank := lacksAssetsCurrent &&
		conceptFiledQuarterly(cf, []string{"Deposits", "DepositsDomestic", "DepositsTotal"})
	isInsuranceConglomerate := lacksAssetsCurrent && !isBank
	bundlesFinancing := conceptFiledQuarterly(cf, []string{"AccruedLiabilitiesCurrent"})

	if !isBank && !isInsuranceConglomerate && !bundlesFinancing {
		return
	}

	financing, hasF := fields["NetCashFlowFromFinancing"]
	common, hasC := fields["NetCashFlowCommon"]
	dividend, hasD := fields["NetCashFlowDividend"]

	// WMT-style filers with a distinct quarterly net-short-term-debt tag
	// keep the direct formula instead of the residual (which would sweep in
	// "other, net" items Sharadar doesn't classify as debt).
	if bundlesFinancing && !isBank && !isInsuranceConglomerate &&
		conceptFiledQuarterly(cf, []string{
			"ProceedsFromRepaymentsOfShortTermDebt",
			"ProceedsFromRepaymentsOfShortTermDebtMaturingInThreeMonthsOrLess",
		}) {
		return
	}

	if hasF && hasC && hasD {
		fields["NetCashFlowDebt"] = financing - common - dividend
	}

	// For banks, recompute derived income fields from Q4 components to keep
	// EBT/EBIT/EBITDA consistent when NetIncome and the Q1-Q3 EBT sum take
	// different cumulative paths.
	if isBank {
		if ni, hasNI := fields["NetIncome"]; hasNI {
			tax := fields["IncomeTaxExpense"]
			intExp := fields["InterestExpense"]
			da := fields["DepreciationAmortizationAndAccretion"]

			fields["EBT"] = ni + tax
			fields["EBIT"] = ni + tax + intExp
			fields["EBITDA"] = ni + tax + intExp + da

			// Use NetIncomeCommonStock (after preferred), not NetIncome:
			// some banks (GS) resolve NetIncome to NetIncomeLoss (before
			// preferred), which would double-count the preferred deduction.
			if consolidated, hasCons := fields["ConsolidatedIncome"]; hasCons {
				niCommon, hasNIC := fields["NetIncomeCommonStock"]
				if !hasNIC {
					niCommon = ni
				}

				pref := fields["PreferredDividendsIncomeStatementImpact"]
				fields["NetIncomeToNonControllingInterests"] = consolidated - niCommon - pref
			}
		}
	}

	// For banks, compute NCFDEBT directly from de-cumulated bank-specific
	// fields. The residual fails because the financing total includes
	// deposits, preferred stock, and other non-debt items.
	if isBank {
		// GS-style banks file financing cash flows as separate
		// unsecured/secured tranches plus extension short-term-net concepts.
		_, isGSStyle := cf.Facts["CustomerAndOtherPayables"]

		var bankDebtFields []struct {
			name string
			sign float64
		}

		if isGSStyle {
			// NB: PaymentsForProceedsFromDerivativeInstrumentFinancingActivities
			// is presented in GS's financing section but Sharadar classifies
			// it as "other financing" rather than debt, so it is NOT added
			// here.
			bankDebtFields = []struct {
				name string
				sign float64
			}{
				{"_bdUnsecuredDebtProceeds", 1},
				{"_bdUnsecuredDebtRepayments", -1},
				{"_bdSecuredDebtProceeds", 1},
				{"_bdSecuredDebtRepayments", -1},
				{"_bdUnsecuredSTDebtNet", 1},
				{"_bdOtherSecuredSTDebtNet", 1},
			}
		} else {
			bankDebtFields = []struct {
				name string
				sign float64
			}{
				{"_bankFedFundsChange", 1},
				{"_bankLTDebtProceeds", 1},
				{"_bankLTDebtRepayments", -1},
				{"_bankSTDebtProceeds", 1},
			}
		}

		ncfDebt := 0.0
		found := false

		for _, f := range bankDebtFields {
			if v, ok := fields[f.name]; ok {
				ncfDebt += f.sign * v
				found = true
			}
		}

		if found {
			fields["NetCashFlowDebt"] = ncfDebt
		}
	}

	// Ensure NetCashFlowBusiness is present in the fields map (even as 0) so
	// the strictFlow TTM check passes. Without this, quarters where no
	// acquisitions occurred have the field absent, causing the MRT TTM to
	// skip it entirely (found < 4 with strictFlow=true). For insurance
	// conglomerates like BRK/B, also zero-fill NetCashFlowCommon: Q1 2025
	// had no repurchases/issuances/tax-withholding, so all operands of the
	// derived field were absent and the formula returned NULL.
	if isBank || isInsuranceConglomerate {
		if _, ok := fields["NetCashFlowBusiness"]; !ok {
			fields["NetCashFlowBusiness"] = 0
		}
	}

	if isInsuranceConglomerate {
		if _, ok := fields["NetCashFlowCommon"]; !ok {
			fields["NetCashFlowCommon"] = 0
		}
	}

	// GS-style investment banks present "Total operating expenses" as
	// NoninterestExpense + ProvisionForLoanLeaseAndOtherLosses on the face of
	// the income statement (unlike commercial banks which keep provision as a
	// separate line). Sharadar's OperatingExpenses matches this face-of-the-
	// statement total, so add provision when the _bdBankProvision field was
	// resolved (gated by the CustomerAndOtherPayables marker). Using the
	// resolved field rather than re-reading from cf ensures the quarterly
	// de-cumulated value flows into Q4 synthesis correctly. Set a sentinel so
	// a second call to this function (sec.go now calls it on quarter maps
	// before annual averages AND again before emission) does not double-add.
	if isBank {
		if provision, ok := fields["_bdBankProvision"]; ok {
			if _, alreadyApplied := fields["_bdBankProvisionApplied"]; !alreadyApplied {
				if opex, hasOE := fields["OperatingExpenses"]; hasOE {
					fields["OperatingExpenses"] = opex + provision
					fields["_bdBankProvisionApplied"] = 1
				}
			}
		}
	}

	// For banks, derive OperatingIncome = GrossProfit - OperatingExpenses
	// when OperatingIncomeLoss isn't available. OperatingExpenses resolves
	// from NoninterestExpense FallbackTag, and GrossProfit = Revenues.
	if isBank {
		if _, hasOI := fields["OperatingIncome"]; !hasOI {
			gp, hasGP := fields["GrossProfit"]
			opex, hasOE := fields["OperatingExpenses"]

			if hasGP && hasOE {
				fields["OperatingIncome"] = gp - opex
			}
		}
	}

	// For banks, also recompute NetCashFlowInvest as the residual of the
	// investing section: invest = total_investing - capex. Bank investing
	// activities include loan originations, fed funds, and repo transactions
	// that standard investment-security tags don't capture. Business
	// acquisitions are already part of the investing total and are tracked
	// separately as NetCashFlowBusiness.
	if isBank {
		investing, hasI := fields["NetCashFlowFromInvesting"]
		if hasI {
			capex := fields["CapitalExpenditure"]     // 0 when absent (banks)
			otherInv := fields["_bankOtherInvesting"] // 0 when absent
			biz := fields["NetCashFlowBusiness"]      // 0 when absent; already negated
			// Add back "other" investing and subtract business acquisitions
			// (both are in the investing total but Sharadar classifies them
			// separately as NCFBIZ and other, not NCFINV).
			fields["NetCashFlowInvest"] = investing - capex + otherInv - biz
		}
	}

	// For banks, override balance sheet fields from extension tags. The
	// mapping-level FallbackTags work for AR dimensions but the MR filing
	// date filter can exclude extension facts from the MR CompanyFacts.
	// Resolving directly from the UNFILTERED cf ensures extension tags are
	// always available.
	if isBank {
		bankExtTags := []struct {
			field string
			tags  []string
		}{
			// JPM 10-K and 10-Q use different casing for extension concepts.
			{"Intangibles", []string{
				"GoodwillServicingAssetsAtFairValueAndOtherIntangibleAssets", // 10-Q
				"GoodwillServicingAssetsatFairValueandOtherIntangibleAssets", // 10-K
			}},
			{"PropertyPlantAndEquipmentNet", []string{
				"PropertyPlantAndEquipmentAndOperatingLeaseRightOfUseAssetAfterAccumulatedDepreciationAndAmortization",
			}},
			{"Receivables", []string{"AccruedInterestAndAccountsReceivable"}},
			// GS reports customer-and-other payables as the balance sheet
			// payables line; us-gaap AccountsPayable* tags are absent.
			{"Payables", []string{"CustomerAndOtherPayables"}},
		}

		for _, ext := range bankExtTags {
			if v, ok := ResolveDirect(cf, FieldMapping{XBRLTags: ext.tags}, periodEnd, formType); ok {
				fields[ext.field] = v
			}
		}

		// GS-style receivables: the "Customer and other receivables" balance
		// sheet line maps to us-gaap:OtherReceivables. The standard chain
		// resolves to NotesReceivableNet (loans receivable), which is a
		// different line for GS. Gate on CustomerAndOtherPayables to avoid
		// affecting other banks that also file OtherReceivables but resolve
		// correctly via the default path.
		if _, ok := cf.Facts["CustomerAndOtherPayables"]; ok {
			if v, rok := ResolveDirect(cf, FieldMapping{XBRLTags: []string{"OtherReceivables"}}, periodEnd, formType); rok {
				fields["Receivables"] = v
			}
		}

		// GS-style total debt: Sharadar sums the interest-bearing wholesale
		// liabilities visible on the balance sheet — unsecured borrowings
		// (short + long), collateralized financings (repo + securities loaned
		// + other secured), and trading liabilities. Deposits are tracked in
		// the separate `deposits` field. The default bank formula
		// (ShortTermBorrowings + LongTermDebt + FedFundsPurchased) doesn't
		// cover GS because GS reports UnsecuredLongTermDebt/UnsecuredDebtCurrent
		// rather than LongTermDebt, and its repo/sec-loaned/trading-liab lines
		// are separate. Gate on CustomerAndOtherPayables so JPM/BAC (which
		// match via the default formula) aren't disturbed.
		if _, ok := cf.Facts["CustomerAndOtherPayables"]; ok {
			gsDebtTags := []string{
				"DebtLongtermAndShorttermCombinedAmount",
				"SecuritiesSoldUnderAgreementsToRepurchase",
				"SecuritiesLoaned",
				"OtherSecuredFinancings",
				"TradingLiabilities",
			}

			total := 0.0
			found := false

			for _, tag := range gsDebtTags {
				if v, tok := ResolveDirect(cf, FieldMapping{XBRLTags: []string{tag}}, periodEnd, formType); tok {
					total += v
					found = true
				}
			}

			if found {
				fields["TotalDebt"] = total

				// Recompute fields that depend on TotalDebt:
				// InvestedCapital = TotalDebt + TotalAssets - Intangibles - Cash - CurrentLiabilities
				// (banks have CurrentLiabilities = 0).
				if ta, hasTA := fields["TotalAssets"]; hasTA {
					intang := fields["Intangibles"]
					cash := fields["CashAndEquivalents"]
					curLiab := fields["CurrentLiabilities"]
					fields["InvestedCapital"] = total + ta - intang - cash - curLiab
				}
			}
		}
	}

	// For banks, compute SGA. Commercial banks (JPM, BAC, WFC): SGA =
	// NoninterestExpense - OtherNoninterestExpense (Sharadar excludes the
	// catch-all "Other" bucket of FDIC assessments, regulatory fees,
	// litigation). GS-style investment banks: SGA = NoninterestExpense -
	// DepreciationAndAmortization - ProfessionalFees -- Sharadar classifies
	// both D&A and professional fees outside SGA for these filers. For GS
	// the OperatingExpenses field has Provision added in the step above, so
	// subtract it back out to get raw NoninterestExpense before applying the
	// GS-specific deductions.
	if isBank {
		nonintExp, hasNIE := fields["OperatingExpenses"] // resolved from NoninterestExpense FallbackTag
		if hasNIE {
			if _, isGSStyle := cf.Facts["CustomerAndOtherPayables"]; isGSStyle {
				rawNIE := nonintExp - fields["_bdBankProvision"] // undo provision addition
				da := fields["DepreciationAmortizationAndAccretion"]
				profFees := fields["_bdProfessionalFees"]
				fields["SellingGeneralAndAdministrativeExpense"] = rawNIE - da - profFees
			} else {
				otherNIE := fields["_bankOtherNoninterestExpense"] // 0 when absent
				fields["SellingGeneralAndAdministrativeExpense"] = nonintExp - otherNIE
			}
		}
	}

	// For banks, compute Investments as the balance sheet residual:
	// TotalAssets - CashAndEquivalents - Receivables - PP&E - Intangibles - OtherAssets.
	// This captures loans, securities, fed funds, and other invested assets.
	if isBank {
		totalAssets, hasTA := fields["TotalAssets"]

		if hasTA {
			cash := fields["CashAndEquivalents"]
			recv := fields["Receivables"]
			ppe := fields["PropertyPlantAndEquipmentNet"]
			intang := fields["Intangibles"]
			oa := 0.0

			if v, ok := ResolveDirect(cf, FieldMapping{XBRLTags: []string{"OtherAssets"}}, periodEnd, formType); ok {
				oa = v
			}

			fields["Investments"] = totalAssets - cash - recv - ppe - intang - oa
		}
	}
}

// deriveCostOfRevenueBottomUp reconstructs CostOfRevenue/GrossProfit/OpEx
// when CostOfRevenue is missing but OperatingIncome and SGA are available
// (insurance/conglomerate filers like BRK/B).
func deriveCostOfRevenueBottomUp(fields map[string]float64) {
	// Only apply when CostOfRevenue is missing and OperatingIncome is present.
	if _, hasCOR := fields["CostOfRevenue"]; hasCOR {
		return
	}

	opIncome, hasOI := fields["OperatingIncome"]
	if !hasOI {
		return
	}

	sga := fields["SellingGeneralAndAdministrativeExpense"] // 0 when absent
	rnd := fields["RandDExpenses"]                          // 0 when absent
	otherExp := fields["_otherExpenses"]                    // 0 when absent; Railroad segment for BRK

	// Need at least SGA to derive the income statement.
	if sga == 0 {
		return
	}

	opEx := sga + rnd + otherExp
	grossProfit := opIncome + opEx
	revenue, hasRev := fields["Revenues"]

	if !hasRev || revenue == 0 {
		return
	}

	costOfRevenue := revenue - grossProfit

	// Only the lower bound is checked: insurance/conglomerate revenue can
	// include investment losses that legitimately push COGS above revenue.
	if costOfRevenue < 0 {
		return
	}

	fields["CostOfRevenue"] = costOfRevenue
	fields["GrossProfit"] = grossProfit
	fields["OperatingExpenses"] = opEx

	// Recompute GrossMargin against the corrected GrossProfit.
	if revenue != 0 {
		fields["GrossMargin"] = math.Round(grossProfit/revenue*1000) / 1000
	}
}

// deriveCostOfRevenueForSegmentFiler handles MCD-style restaurant filers
// whose CostOfGoodsAndServicesSold is only a partial slice. Gate:
// SegmentReportingOtherItemAmount filed quarterly.
func deriveCostOfRevenueForSegmentFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"SegmentReportingOtherItemAmount"}) {
		return
	}

	costsAndExpenses, okCE := fields["_costsAndExpensesRaw"]
	sga, okSGA := fields["_sgaBroad"]
	segmentOther, okSeg := fields["_segmentReportingOtherItem"]
	revenues, okRev := fields["Revenues"]

	if !okCE || !okSGA || !okSeg || !okRev || revenues == 0 {
		return
	}

	opEx := sga + segmentOther

	costOfRevenue := costsAndExpenses - opEx
	if costOfRevenue < 0 {
		return
	}

	grossProfit := revenues - costOfRevenue

	fields["CostOfRevenue"] = costOfRevenue
	fields["OperatingExpenses"] = opEx
	fields["GrossProfit"] = grossProfit
	fields["GrossMargin"] = math.Round(grossProfit/revenues*1000) / 1000
}

// deriveCostOfRevenueForEnergyFiler handles XOM-style integrated oil/gas
// filers whose CostsAndExpenses bundles SGA, D&A, exploration, pension,
// interest. Runs after deriveCostOfRevenueBottomUp and overrides its
// SGA-only OpEx when ExplorationExpense is filed quarterly.
func deriveCostOfRevenueForEnergyFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"ExplorationExpense"}) {
		return
	}

	costsAndExpenses, okCE := fields["_costsAndExpensesRaw"]

	revenues, okRev := fields["Revenues"]
	if !okCE || !okRev || revenues == 0 {
		return
	}

	sga := fields["SellingGeneralAndAdministrativeExpense"]
	dda := fields["_depreciationDepletionAndAmortization"]
	exploration := fields["_explorationExpense"]
	nonServicePension := fields["_nonServicePensionExpense"]
	interestExp := fields["InterestExpense"]

	opEx := sga + dda + exploration + nonServicePension

	costOfRevenue := costsAndExpenses - opEx - interestExp
	if costOfRevenue < 0 {
		return
	}

	grossProfit := revenues - costOfRevenue

	fields["CostOfRevenue"] = costOfRevenue
	fields["OperatingExpenses"] = opEx
	fields["GrossProfit"] = grossProfit
	fields["GrossMargin"] = math.Round(grossProfit/revenues*1000) / 1000
}

// deriveCostOfRevenueForFullCostEnergyFiler handles BATL-style small E&P
// companies reporting under the full-cost method. The gate is presence of
// OilAndGasPropertyFullCostMethodNet on a recent 10-Q.
func deriveCostOfRevenueForFullCostEnergyFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"OilAndGasPropertyFullCostMethodNet"}) {
		return
	}

	revenues, okRev := fields["Revenues"]
	if !okRev || revenues == 0 {
		return
	}

	leaseOperating := fields["_fcEnergyLeaseOperating"]
	workover := fields["_fcEnergyWorkover"]
	productionTax := fields["_fcEnergyProductionTax"]
	assetImpair := fields["_fcEnergyAssetImpairment"]
	gna := fields["_generalAndAdministrativeExpense"]
	dda := fields["DepreciationAmortizationAndAccretion"]

	costOfRevenue := leaseOperating + workover
	opEx := gna + dda + assetImpair + productionTax

	if costOfRevenue == 0 && opEx == 0 {
		return
	}

	grossProfit := revenues - costOfRevenue
	opIncome := grossProfit - opEx

	fields["CostOfRevenue"] = costOfRevenue
	fields["OperatingExpenses"] = opEx
	fields["GrossProfit"] = grossProfit
	fields["OperatingIncome"] = opIncome
	fields["GrossMargin"] = math.Round(grossProfit/revenues*1000) / 1000
}

// deriveCostOfRevenueForIndustrialFinancialFiler handles CAT-style filers by
// replacing the small CostOfGoodsAndServicesSold residual with
// CostsAndExpenses - (SGA + R&D + abs(OtherOperating)).
func deriveCostOfRevenueForIndustrialFinancialFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"CostOfRevenue"}) {
		return
	}

	if !conceptFiledQuarterly(cf, []string{"OtherOperatingIncomeExpenseNet"}) {
		return
	}

	if !conceptFiledQuarterly(cf, []string{"CostsAndExpenses"}) {
		return
	}

	costsAndExpenses, okCE := fields["_costsAndExpensesRaw"]
	revenues, okRev := fields["Revenues"]

	if !okCE || !okRev || revenues == 0 {
		return
	}

	sga := fields["SellingGeneralAndAdministrativeExpense"]
	rnd := fields["RandDExpenses"]
	otherOp := fields["_otherOperatingIncomeExpenseNet"]

	if sga == 0 {
		return
	}

	opEx := sga + rnd + math.Abs(otherOp)

	costOfRevenue := costsAndExpenses - opEx
	if costOfRevenue < 0 {
		return
	}

	grossProfit := revenues - costOfRevenue

	fields["CostOfRevenue"] = costOfRevenue
	fields["OperatingExpenses"] = opEx
	fields["GrossProfit"] = grossProfit
	fields["GrossMargin"] = math.Round(grossProfit/revenues*1000) / 1000
}

// overrideLiabilitiesForFullCostEnergyFiler adds BATL-style ARO, derivative,
// and mezzanine-equity balances to liabilities_non_current. Gate:
// OilAndGasPropertyFullCostMethodNet filed quarterly.
func overrideLiabilitiesForFullCostEnergyFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"OilAndGasPropertyFullCostMethodNet"}) {
		return
	}

	aro := fields["_fcEnergyARO"]
	derivNC := fields["_fcEnergyDerivLiabNC"]
	tempEquity := fields["_fcEnergyTemporaryEquity"]

	addition := aro + derivNC + tempEquity
	if addition == 0 {
		return
	}

	if v, ok := fields["LiabilitiesNonCurrent"]; ok {
		fields["LiabilitiesNonCurrent"] = v + addition
	}

	if v, ok := fields["TotalLiabilities"]; ok {
		fields["TotalLiabilities"] = v + addition
	}

	// DebtToEquityRatio = TotalLiabilities / Equity — recompute against the
	// updated TotalLiabilities.
	if totalLiab, ok := fields["TotalLiabilities"]; ok {
		if equity, ok := fields["Equity"]; ok && equity != 0 {
			fields["DebtToEquityRatio"] = math.Round(totalLiab/equity*1000) / 1000
		}
	}
}

// overrideNCIForFullCostEnergyFiler zeros NCI and rolls the residual into
// preferred_dividends for BATL-style filers without consolidated subsidiaries
// (no NetIncomeLossAttributableToNoncontrollingInterest tag).
func overrideNCIForFullCostEnergyFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"OilAndGasPropertyFullCostMethodNet"}) {
		return
	}

	if conceptFiledQuarterly(cf, []string{"NetIncomeLossAttributableToNoncontrollingInterest"}) {
		return
	}

	ci, hasCI := fields["ConsolidatedIncome"]
	nic, hasNIC := fields["NetIncomeCommonStock"]

	if !hasCI || !hasNIC {
		return
	}

	totalDeduction := ci - nic
	fields["PreferredDividendsIncomeStatementImpact"] = totalDeduction
	fields["NetIncomeToNonControllingInterests"] = 0
}

// overrideInvestingClassificationForFullCostEnergyFiler moves
// PaymentsToAcquireOtherProductiveAssets and PaymentsToAcquireOilAndGasProperty
// into capex, then recomputes FreeCashFlow.
func overrideInvestingClassificationForFullCostEnergyFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"OilAndGasPropertyFullCostMethodNet"}) {
		return
	}

	otherProductive := fields["_fcEnergyOtherProductiveAssets"]
	ogAcq := fields["_fcEnergyOilGasPropertyAcq"]

	if capex, ok := fields["CapitalExpenditure"]; ok {
		fields["CapitalExpenditure"] = capex - otherProductive - ogAcq
	}

	if ncfBiz, ok := fields["NetCashFlowBusiness"]; ok {
		// Reverse the -1 coefficient on _paymentsOtherProductiveAssets so it
		// appears only in capex.
		fields["NetCashFlowBusiness"] = ncfBiz + otherProductive
	}

	if ncfOps, ok := fields["NetCashFlowFromOperations"]; ok {
		if capex, ok := fields["CapitalExpenditure"]; ok {
			fcf := ncfOps + capex
			fields["FreeCashFlow"] = fcf

			if was, ok := fields["WeightedAverageShares"]; ok && was != 0 {
				fields["FreeCashFlowPerShare"] = math.Round(fcf/was*1000) / 1000
			}
		}
	}
}

// overrideInterestExpenseForFullCostEnergyFiler swaps in the filer's
// "Interest expense and other" line (a company extension) for InterestExpense.
// Gate: OilAndGasPropertyFullCostMethodNet filed quarterly.
func overrideInterestExpenseForFullCostEnergyFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"OilAndGasPropertyFullCostMethodNet"}) {
		return
	}

	val, ok := fields["_fcEnergyInterestExpense"]
	if !ok {
		return
	}

	fields["InterestExpense"] = val

	// Recompute EBIT/EBITDA against the updated InterestExpense.
	netIncome, hasNI := fields["NetIncome"]
	taxExp := fields["IncomeTaxExpense"]

	if hasNI {
		ebit := netIncome + taxExp + val
		fields["EBIT"] = ebit

		rev, hasRev := fields["Revenues"]

		// Only recompute when already populated; ResolveAllFields skips
		// the ratio for quarterly dimensions where Sharadar reports 0.
		if _, hasROS := fields["ReturnOnSales"]; hasROS && hasRev && rev != 0 {
			fields["ReturnOnSales"] = math.Round(ebit/rev*1000) / 1000
		}

		if dda, ok := fields["DepreciationAmortizationAndAccretion"]; ok {
			ebitda := ebit + dda
			fields["EBITDA"] = ebitda

			if _, hasMargin := fields["EBITDAMargin"]; hasMargin && hasRev && rev != 0 {
				fields["EBITDAMargin"] = math.Round(ebitda/rev*1000) / 1000
			}
		}

		// Only recompute ROIC when ResolveAllFields already produced one.
		if _, hasROIC := fields["ROIC"]; hasROIC {
			if ic, ok := fields["InvestedCapitalAverage"]; ok && ic != 0 {
				fields["ROIC"] = math.Round(ebit/ic*1000) / 1000
			} else if ic, ok := fields["InvestedCapital"]; ok && ic != 0 {
				fields["ROIC"] = math.Round(ebit/ic*1000) / 1000
			}
		}
	}
}

// overrideNCFBusinessAsResidualForReceivablesFiler computes NetCashFlowBusiness
// as the investing-section residual for XOM-style filers without explicit
// business-acquisition tags. Gate: ProceedsFromSaleAndCollectionOfReceivables
// filed quarterly.
func overrideNCFBusinessAsResidualForReceivablesFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"ProceedsFromSaleAndCollectionOfReceivables"}) {
		return
	}

	ncfInvesting, hasNCFI := fields["NetCashFlowFromInvesting"]
	ncfInvest, hasInvest := fields["NetCashFlowInvest"]
	capex, hasCapex := fields["CapitalExpenditure"]

	if !hasNCFI || !hasInvest || !hasCapex {
		return
	}

	rec := fields["_proceedsReceivablesCollection"]

	fields["NetCashFlowBusiness"] = ncfInvesting - ncfInvest - capex - rec
}

func deriveCostOfRevenueForRestaurantFiler(cf *CompanyFacts, fields map[string]float64) {
	if !conceptFiledQuarterly(cf, []string{"PreOpeningCosts"}) {
		return
	}

	costsAndExpenses, okCE := fields["_costsAndExpensesRaw"]
	revenues, okRev := fields["Revenues"]

	if !okCE || !okRev || revenues == 0 {
		return
	}

	rent, okRent := fields["_rentInCostOfRevenue"]
	if !okRent {
		return
	}

	gna := fields["_generalAndAdministrativeExpense"]
	depAmor := fields["_depreciationDepletionAndAmortization"]
	preOpen := fields["_preOpeningCosts"]
	impair := fields["_restaurantImpairmentProvisions"]

	opEx := gna + depAmor + preOpen + impair + rent

	costOfRevenue := costsAndExpenses - opEx
	if costOfRevenue < 0 {
		return
	}

	grossProfit := revenues - costOfRevenue

	fields["CostOfRevenue"] = costOfRevenue
	fields["OperatingExpenses"] = opEx
	fields["GrossProfit"] = grossProfit
	fields["GrossMargin"] = math.Round(grossProfit/revenues*1000) / 1000
}
