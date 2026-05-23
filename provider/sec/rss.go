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
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/html/charset"
)

// FilingEntry represents a single filing discovered from the EDGAR ATOM feed.
type FilingEntry struct {
	CIK         int
	FormType    string
	Filed       time.Time
	Accn        string // SEC accession number, matches Fact.Accn naming
	CompanyName string
}

// edgarFeed represents the EDGAR ATOM feed structure.
type edgarFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []feedEntry `xml:"entry"`
}

type feedEntry struct {
	Title    string       `xml:"title"`
	Updated  string       `xml:"updated"`
	Link     feedLink     `xml:"link"`
	Summary  feedSummary  `xml:"summary"`
	Category feedCategory `xml:"category"`
}

type feedLink struct {
	Href string `xml:"href,attr"`
}

type feedSummary struct {
	Content string `xml:",chardata"`
}

type feedCategory struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr"`
}

// EDGAR titles look like: "10-K - Company Name (0001234567) (Filer)".
var titleRegex = regexp.MustCompile(`^[^-]+-\s*(.+?)\s*\(\d+\)\s*\(.*\)\s*$`)

// EDGAR summary blobs look like: "...AccNo:</b> 0001493152-26-015327...".
var summaryAccnRegex = regexp.MustCompile(`AccNo:?\s*</?b>?\s*([0-9]{10}-[0-9]{2}-[0-9]{6})`)

// accnNumberRegex matches a bare EDGAR accession number (NNNNNNNNNN-NN-NNNNNN).
var accnNumberRegex = regexp.MustCompile(`^[0-9]{10}-[0-9]{2}-[0-9]{6}$`)

// ParseFilingFeed parses an EDGAR ATOM feed and returns 10-K and 10-Q filings
// (amendments excluded). Uses a charset-aware decoder since EDGAR feeds may
// declare a non-UTF-8 encoding (commonly ISO-8859-1).
func ParseFilingFeed(xmlData []byte) ([]FilingEntry, error) {
	decoder := xml.NewDecoder(bytes.NewReader(xmlData))
	decoder.CharsetReader = charset.NewReaderLabel

	var feed edgarFeed
	if err := decoder.Decode(&feed); err != nil {
		return nil, fmt.Errorf("parsing EDGAR feed XML: %w", err)
	}

	filings := make([]FilingEntry, 0, len(feed.Entries))

	for _, entry := range feed.Entries {
		formType := strings.TrimSpace(entry.Category.Term)
		if formType != "10-K" && formType != "10-Q" {
			continue
		}

		cik := extractCIKFromLink(entry.Link.Href)
		if cik == 0 {
			log.Warn().Str("link", entry.Link.Href).Msg("could not extract CIK from feed entry link")
			continue
		}

		filed, err := time.Parse(time.RFC3339, strings.TrimSpace(entry.Updated))
		if err != nil {
			log.Warn().Err(err).Str("updated", entry.Updated).Msg("could not parse filing updated timestamp")
		}

		accn := extractAccnFromLink(entry.Link.Href)
		if accn == "" {
			accn = extractAccnFromSummary(entry.Summary.Content)
		}

		filings = append(filings, FilingEntry{
			CIK:         cik,
			FormType:    formType,
			Filed:       filed,
			Accn:        accn,
			CompanyName: extractCompanyName(entry.Title),
		})
	}

	return filings, nil
}

// extractCIKFromLink extracts the CIK from an EDGAR URL path (.../data/<cik>/...).
func extractCIKFromLink(href string) int {
	parts := strings.Split(href, "/")
	for i, p := range parts {
		if p == "data" && i+1 < len(parts) {
			cik, err := strconv.Atoi(parts[i+1])
			if err == nil {
				return cik
			}
		}
	}

	return 0
}

// extractAccnFromLink extracts an accession number from an EDGAR index URL
// (.../<accn>-index.htm).
func extractAccnFromLink(href string) string {
	parts := strings.Split(href, "/")
	if len(parts) == 0 {
		return ""
	}

	last := parts[len(parts)-1]
	last = strings.TrimSuffix(last, "-index.htm")
	last = strings.TrimSuffix(last, "-index.html")

	if accnNumberRegex.MatchString(last) {
		return last
	}

	return ""
}

// extractAccnFromSummary parses the accession number out of an EDGAR summary blob.
func extractAccnFromSummary(summary string) string {
	matches := summaryAccnRegex.FindStringSubmatch(summary)
	if len(matches) >= 2 {
		return matches[1]
	}

	return ""
}

// extractCompanyName extracts the company name from an EDGAR feed entry
// title. Falls back to the trimmed title when the expected pattern is absent.
func extractCompanyName(title string) string {
	matches := titleRegex.FindStringSubmatch(title)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}

	return strings.TrimSpace(title)
}
