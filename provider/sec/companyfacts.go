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
	"fmt"
	"time"

	"github.com/tidwall/gjson"
)

// Fact represents a single XBRL fact value from an SEC filing.
type Fact struct {
	End   time.Time // Period end date (always present)
	Start time.Time // Period start date (present for duration concepts; zero for instant concepts)
	Filed time.Time // Date the filing was submitted to SEC
	Val   float64   // The reported value
	Accn  string    // SEC accession number
	Form  string    // Filing form type (10-K, 10-Q)
	FP    string    // Fiscal period (FY, Q1, Q2, Q3, Q4)
	Frame string    // XBRL frame identifier (e.g. CY2023Q3I)
	FY    int       // Fiscal year
}

// CompanyFacts holds parsed SEC EDGAR companyfacts data for a single entity.
type CompanyFacts struct {
	CIK        int               // Central Index Key
	EntityName string            // Company name
	Facts      map[string][]Fact // Map of concept name to facts (e.g. "Assets" -> []Fact)
}

// unitPreference defines the priority order for selecting unit types.
// Lower index = higher preference.
var unitPreference = []string{"USD", "USD/shares", "shares", "pure"}

const dateFormat = "2006-01-02"

// ParseCompanyFacts parses SEC EDGAR companyfacts JSON into a CompanyFacts struct.
// It only includes facts from 10-K and 10-Q filings and selects the preferred
// unit type when multiple are available for a concept.
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

	// Iterate over us-gaap concepts
	usGAAP := root.Get("facts.us-gaap")
	if !usGAAP.Exists() {
		return cf, nil
	}

	usGAAP.ForEach(func(conceptName, conceptData gjson.Result) bool {
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

		// If no preferred unit found, try the first available unit
		if !selectedUnit.Exists() {
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
			cf.Facts[conceptName.String()] = facts
		}

		return true
	})

	return cf, nil
}
