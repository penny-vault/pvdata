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
package data

import (
	"fmt"
	"strings"
)

// GenerateViewSQL produces the single SQL statement (CREATE OR REPLACE VIEW
// or DROP VIEW IF EXISTS) for a published view of this data type backed by
// the given ordered sources. Source order is meaningful only when
// DedupKeys is non-empty; for plain unions the order has no effect on the
// rows returned but is preserved verbatim in the leg sequence for
// readability.
func (dt *DataType) GenerateViewSQL(viewName string, sources []ViewSource) string {
	if len(sources) == 0 {
		return fmt.Sprintf("DROP VIEW IF EXISTS %s", viewName)
	}

	if len(dt.DedupKeys) > 0 {
		return dt.buildDedupedUnion(viewName, sources)
	}

	return dt.buildPlainUnion(viewName, sources)
}

func (dt *DataType) buildPlainUnion(viewName string, sources []ViewSource) string {
	legs := make([]string, len(sources))

	for i, s := range sources {
		legs[i] = dt.buildLeg(s)
	}

	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", viewName, strings.Join(legs, " UNION ALL "))
}

// buildLeg renders a single SELECT/FROM[/WHERE] leg. The SELECT/FROM portion
// comes from ViewGenerator if set, otherwise defaults to "SELECT * FROM <t>".
// Date bounds are appended only when DateColumn is non-empty.
func (dt *DataType) buildLeg(s ViewSource) string {
	var sel string

	if dt.ViewGenerator != nil {
		sel = dt.ViewGenerator.SelectFrom(s.TableName)
	} else {
		sel = fmt.Sprintf("SELECT * FROM %s", s.TableName)
	}

	where := dt.buildWhereClause(s)
	if where == "" {
		return sel
	}

	return sel + " WHERE " + where
}

func (dt *DataType) buildWhereClause(s ViewSource) string {
	if dt.DateColumn == "" {
		return ""
	}

	var parts []string

	if s.FromDate != nil {
		parts = append(parts, fmt.Sprintf("%s >= '%s'", dt.DateColumn, s.FromDate.Format("2006-01-02")))
	}

	if s.UntilDate != nil {
		parts = append(parts, fmt.Sprintf("%s < '%s'", dt.DateColumn, s.UntilDate.Format("2006-01-02")))
	}

	return strings.Join(parts, " AND ")
}

// buildDedupedUnion emits a CREATE OR REPLACE VIEW where each lower-priority
// leg excludes rows whose dedup-key tuple already appears in any
// higher-priority leg. The resulting view returns each unique dedup-key
// tuple exactly once, taking the row whole from the highest-priority source
// containing it.
//
// Date bounds (FromDate/UntilDate) are not applied in dedup mode -- there is
// no real-world data type today that uses both DedupKeys and a DateColumn,
// and the asset case explicitly has DateColumn == "". A non-nil
// ViewGenerator is honored for the SELECT portion of each leg; the
// NOT EXISTS clauses always qualify dedup keys with the bare table name, so
// the generator must not introduce a table alias (i.e. emit
// "SELECT cols FROM <table>" with no AS).
func (dt *DataType) buildDedupedUnion(viewName string, sources []ViewSource) string {
	legs := make([]string, len(sources))

	for i, s := range sources {
		legs[i] = dt.buildDedupLeg(s.TableName, sources[:i])
	}

	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", viewName, strings.Join(legs, " UNION ALL "))
}

func (dt *DataType) buildDedupLeg(table string, higher []ViewSource) string {
	var sel string
	if dt.ViewGenerator != nil {
		sel = dt.ViewGenerator.SelectFrom(table)
	} else {
		sel = fmt.Sprintf("SELECT * FROM %s", table)
	}

	if len(higher) == 0 {
		return sel
	}

	clauses := make([]string, len(higher))

	for i, h := range higher {
		clauses[i] = fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM %s WHERE %s)",
			h.TableName,
			joinKeyEquality(dt.DedupKeys, h.TableName, table),
		)
	}

	return sel + " WHERE " + strings.Join(clauses, " AND ")
}

// joinKeyEquality returns "<higher>.<k1> = <self>.<k1> AND ..." for each
// dedup key, used inside a NOT EXISTS subquery to identify rows already
// present in a higher-priority leg.
func joinKeyEquality(keys []string, higher, self string) string {
	parts := make([]string, len(keys))

	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s.%s = %s.%s", higher, k, self, k)
	}

	return strings.Join(parts, " AND ")
}
