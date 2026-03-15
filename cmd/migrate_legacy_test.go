package cmd

import (
	"testing"

	"github.com/penny-vault/pvdata/data"
)

func TestRequiredLegacyTables(t *testing.T) {
	if len(requiredLegacyTables) == 0 {
		t.Error("requiredLegacyTables should not be empty")
	}

	for _, tbl := range requiredLegacyTables {
		if tbl == "" {
			t.Error("requiredLegacyTables contains empty string")
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
