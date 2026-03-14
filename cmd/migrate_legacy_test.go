package cmd

import (
	"testing"

	"github.com/penny-vault/pvdata/data"
)

func TestLegacyTablesListIsComplete(t *testing.T) {
	expected := map[string]bool{
		"activity": true, "announcements": true, "assets": true,
		"eod": true, "market_holidays": true, "portfolio_measurements": true,
		"portfolio_transactions": true, "portfolios": true, "profile": true,
		"reported_financials": true, "schema_migrations": true,
		"seeking_alpha": true, "trading_days": true,
		"zacks_financials": true, "zacks_number_1": true,
	}

	if len(legacyTables) != len(expected) {
		t.Errorf("expected %d legacy tables, got %d", len(expected), len(legacyTables))
	}

	for _, tbl := range legacyTables {
		if !expected[tbl] {
			t.Errorf("unexpected table in legacyTables: %s", tbl)
		}
	}
}

func TestRequiredLegacyTablesAreSubsetOfLegacyTables(t *testing.T) {
	legacySet := make(map[string]bool)
	for _, tbl := range legacyTables {
		legacySet[tbl] = true
	}

	for _, tbl := range requiredLegacyTables {
		if !legacySet[tbl] {
			t.Errorf("required table %q is not in legacyTables", tbl)
		}
	}
}

func TestZacksDataTypesMatchProvider(t *testing.T) {
	zacksTypes := []string{data.RatingKey, data.MetricKey, data.EstimateKey, data.ConsensusKey}
	for _, dt := range zacksTypes {
		if data.DataTypes[dt] == nil {
			t.Errorf("data type %q not found in DataTypes registry", dt)
		}
	}
}
