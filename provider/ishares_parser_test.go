package provider

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestISharesParser(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "iShares Parser Suite")
}

var _ = Describe("parseISharesXML", func() {
	sampleXML := []byte(`<?xml version="1.0"?>
<ss:Workbook xmlns:ss="urn:schemas-microsoft-com:office:spreadsheet">
<ss:Worksheet ss:Name="Disclaimers">
<ss:Table></ss:Table>
</ss:Worksheet>
<ss:Worksheet ss:Name="Holdings">
<ss:Table>
<ss:Row>
<ss:Cell ss:StyleID="Left">
<ss:Data ss:Type="String">05-Mar-2026</ss:Data>
</ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left">
<ss:Data ss:Type="String">iShares Russell 1000 Value ETF</ss:Data>
</ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell><ss:Data ss:Type="String">Fund Holdings as of</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Mar 05, 2026</ss:Data></ss:Cell>
</ss:Row>
<ss:Row><ss:Cell><ss:Data ss:Type="String"></ss:Data></ss:Cell></ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Ticker</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Name</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Sector</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Asset Class</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Market Value</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Weight (%)</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Notional Value</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Quantity</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Price</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Location</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Exchange</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Currency</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">FX Rate</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Accrual Date</ss:Data></ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">AAPL</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">APPLE INC</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Information Technology</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Equity</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">500000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">5.25</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">500000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">2000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">250.0</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">United States</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">NASDAQ</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">USD</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1</ss:Data></ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">CASH</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">CASH COLLATERAL</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">-</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Cash and/or Derivatives</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">100000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">0.01</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">100000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">100000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1.0</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">United States</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">-</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">USD</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1</ss:Data></ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">MSFT</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">MICROSOFT CORP</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Information Technology</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Equity</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">400000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">4.20</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">400000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">400.0</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">United States</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">NASDAQ</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">USD</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1</ss:Data></ss:Cell>
</ss:Row>
</ss:Table>
</ss:Worksheet>
</ss:Workbook>`)

	It("parses holdings from XML", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(2))
	})

	It("extracts the snapshot date", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.SnapshotDate.Year()).To(Equal(2026))
		Expect(result.SnapshotDate.Month()).To(Equal(time.March))
		Expect(result.SnapshotDate.Day()).To(Equal(5))
	})

	It("extracts ticker and weight", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())
		var aapl *iSharesHolding
		for _, h := range result.Holdings {
			if h.Ticker == "AAPL" {
				aapl = &h
				break
			}
		}
		Expect(aapl).ToNot(BeNil())
		Expect(aapl.Weight).To(BeNumerically("~", 0.0525, 0.0001))
	})

	It("filters out non-equity holdings", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())
		for _, h := range result.Holdings {
			Expect(h.Ticker).ToNot(Equal("CASH"))
		}
	})
})
