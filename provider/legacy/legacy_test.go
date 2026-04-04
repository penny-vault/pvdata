package legacy_test

import (
	"testing"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/provider"
)

func TestLegacyProvider(t *testing.T) {
	p, ok := provider.Map["legacy"]
	if !ok {
		t.Fatal("legacy provider not registered in Map")
	}

	if p.Name() != "Legacy" {
		t.Errorf("expected name 'Legacy', got %q", p.Name())
	}

	datasets := p.Datasets()

	// Check EOD dataset
	eodDS, ok := datasets["eod"]
	if !ok {
		t.Fatal("missing 'eod' dataset")
	}
	if len(eodDS.DataTypes) != 1 || eodDS.DataTypes[0].Name != data.EODKey {
		t.Errorf("eod dataset should have exactly one data type: eod")
	}
	if eodDS.Fetch != nil {
		t.Error("eod dataset Fetch should be nil")
	}

	// Check assets dataset
	assetsDS, ok := datasets["assets"]
	if !ok {
		t.Fatal("missing 'assets' dataset")
	}
	if len(assetsDS.DataTypes) != 1 || assetsDS.DataTypes[0].Name != data.AssetKey {
		t.Errorf("assets dataset should have exactly one data type: asset-description")
	}

	// Check market-holidays dataset
	mhDS, ok := datasets["market-holidays"]
	if !ok {
		t.Fatal("missing 'market-holidays' dataset")
	}
	if len(mhDS.DataTypes) != 1 || mhDS.DataTypes[0].Name != data.MarketHolidaysKey {
		t.Errorf("market-holidays dataset should have exactly one data type: market-holidays")
	}

	// Check Zacks Screener Data dataset
	zacksDS, ok := datasets["Zacks Screener Data"]
	if !ok {
		t.Fatal("missing 'Zacks Screener Data' dataset")
	}
	expectedTypes := map[string]bool{
		data.RatingKey:    true,
		data.MetricKey:    true,
		data.EstimateKey:  true,
		data.ConsensusKey: true,
	}
	if len(zacksDS.DataTypes) != 4 {
		t.Errorf("expected 4 data types for zacks, got %d", len(zacksDS.DataTypes))
	}
	for _, dt := range zacksDS.DataTypes {
		if !expectedTypes[dt.Name] {
			t.Errorf("unexpected data type %q in zacks dataset", dt.Name)
		}
	}
}
