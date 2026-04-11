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
package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/penny-vault/pvdata/data"
)

func TestFundamentalFieldsMatchSchema(t *testing.T) {
	dt := data.DataTypes[data.FundamentalsKey]
	if dt == nil {
		t.Fatalf("FundamentalsKey data type not registered")
	}

	schema := dt.Schema

	for _, f := range fundamentalFields {
		var want string

		switch f.kind {
		case kindInt:
			want = fmt.Sprintf("%s BIGINT", f.column)
		case kindFloat:
			want = fmt.Sprintf("%s NUMERIC", f.column)
		}

		if !strings.Contains(schema, want) {
			t.Errorf("field %q not found in schema with expected type (looking for %q)", f.column, want)
		}
	}
}

func TestFieldByName(t *testing.T) {
	f, ok := fieldByName("revenues")
	if !ok {
		t.Fatalf("expected to find revenues field")
	}

	if f.kind != kindInt {
		t.Errorf("expected revenues to be kindInt, got %v", f.kind)
	}

	if _, ok := fieldByName("not_a_field"); ok {
		t.Errorf("did not expect to find not_a_field")
	}
}

func TestValuesDifferBothNull(t *testing.T) {
	if valuesDiffer(nil, nil, 0.0001, 0) {
		t.Errorf("two nulls should not differ")
	}
}

func TestValuesDifferOneNull(t *testing.T) {
	v := 5.0

	if !valuesDiffer(&v, nil, 0.0001, 0) {
		t.Errorf("null vs non-null should differ")
	}

	if !valuesDiffer(nil, &v, 0.0001, 0) {
		t.Errorf("non-null vs null should differ")
	}
}

func TestValuesDifferExact(t *testing.T) {
	a, b := 100.0, 100.0

	if valuesDiffer(&a, &b, 0.0001, 0) {
		t.Errorf("equal values should not differ")
	}
}

func TestValuesDifferWithinRelTol(t *testing.T) {
	a, b := 1_000_000.0, 1_000_050.0 // 0.005% difference, under 0.01%

	if valuesDiffer(&a, &b, 0.0001, 0) {
		t.Errorf("0.005%% diff should be within 0.01%% tolerance")
	}
}

func TestValuesDifferOutsideRelTol(t *testing.T) {
	a, b := 1_000_000.0, 1_001_000.0 // 0.1% difference, above 0.01%

	if !valuesDiffer(&a, &b, 0.0001, 0) {
		t.Errorf("0.1%% diff should exceed 0.01%% tolerance")
	}
}

func TestValuesDifferAbsToleranceFloor(t *testing.T) {
	a, b := 0.0, 0.5
	// rel diff would be huge, but abs-tol floor of 1 keeps it within tolerance
	if valuesDiffer(&a, &b, 0.0001, 1.0) {
		t.Errorf("|0-0.5|=0.5 should be within abs-tol=1")
	}
}

func TestValuesDifferBothZero(t *testing.T) {
	a, b := 0.0, 0.0
	if valuesDiffer(&a, &b, 0.0001, 0) {
		t.Errorf("0 vs 0 should not differ")
	}
}

func TestValuesDifferZeroVsSmall(t *testing.T) {
	a, b := 0.0, 0.0001
	// With abs-tol=0 and one value zero, max(|a|,|b|) = 0.0001, |diff|/max = 1.0 > rel-tol
	if !valuesDiffer(&a, &b, 0.0001, 0) {
		t.Errorf("0 vs 0.0001 should differ when abs-tol=0")
	}
}
