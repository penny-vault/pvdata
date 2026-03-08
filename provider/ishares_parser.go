package provider

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

type iSharesHolding struct {
	Ticker string
	Weight float64
}

type iSharesParseResult struct {
	SnapshotDate time.Time
	Holdings     []iSharesHolding
}

type ssWorkbook struct {
	XMLName    xml.Name      `xml:"Workbook"`
	Worksheets []ssWorksheet `xml:"Worksheet"`
}

type ssWorksheet struct {
	Name  string  `xml:"Name,attr"`
	Table ssTable `xml:"Table"`
}

type ssTable struct {
	Rows []ssRow `xml:"Row"`
}

type ssRow struct {
	Cells []ssCell `xml:"Cell"`
}

type ssCell struct {
	StyleID string `xml:"StyleID,attr"`
	Data    ssData `xml:"Data"`
}

type ssData struct {
	Type  string `xml:"Type,attr"`
	Value string `xml:",chardata"`
}

func parseISharesXML(xmlData []byte) (*iSharesParseResult, error) {
	// Strip BOM (possibly duplicated)
	xmlData = bytes.TrimPrefix(xmlData, []byte("\xef\xbb\xbf"))
	xmlData = bytes.TrimPrefix(xmlData, []byte("\xef\xbb\xbf"))

	var workbook ssWorkbook
	if err := xml.Unmarshal(xmlData, &workbook); err != nil {
		return nil, err
	}

	result := &iSharesParseResult{}

	var holdingsSheet *ssWorksheet
	for i := range workbook.Worksheets {
		if workbook.Worksheets[i].Name == "Holdings" {
			holdingsSheet = &workbook.Worksheets[i]
			break
		}
	}
	if holdingsSheet == nil {
		return nil, xml.UnmarshalError("Holdings worksheet not found")
	}

	rows := holdingsSheet.Table.Rows

	// Extract snapshot date from the first row
	if len(rows) > 0 && len(rows[0].Cells) > 0 {
		dateStr := strings.TrimSpace(rows[0].Cells[0].Data.Value)
		if t, err := time.Parse("02-Jan-2006", dateStr); err == nil {
			result.SnapshotDate = t
		}
	}

	// Find header row
	headerIdx := -1
	for i, row := range rows {
		if len(row.Cells) > 0 && row.Cells[0].StyleID == "headerstyle" &&
			row.Cells[0].Data.Value == "Ticker" {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		return result, nil
	}

	// Build column index map
	colIdx := make(map[string]int)
	for i, cell := range rows[headerIdx].Cells {
		colIdx[cell.Data.Value] = i
	}

	tickerCol := colIdx["Ticker"]
	assetClassCol := colIdx["Asset Class"]
	weightCol := colIdx["Weight (%)"]

	for _, row := range rows[headerIdx+1:] {
		if len(row.Cells) <= weightCol {
			continue
		}

		assetClass := row.Cells[assetClassCol].Data.Value
		if assetClass != "Equity" {
			continue
		}

		ticker := strings.TrimSpace(row.Cells[tickerCol].Data.Value)
		if ticker == "" {
			continue
		}

		weightPct, err := strconv.ParseFloat(row.Cells[weightCol].Data.Value, 64)
		if err != nil {
			continue
		}

		result.Holdings = append(result.Holdings, iSharesHolding{
			Ticker: ticker,
			Weight: weightPct / 100.0,
		})
	}

	return result, nil
}
