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
