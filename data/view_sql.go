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

// buildDedupedUnion is implemented in Task 4. For now, fall back to the plain
// union form so any caller that flips DedupKeys on early gets correct
// non-dedup behavior rather than a broken stub.
func (dt *DataType) buildDedupedUnion(viewName string, sources []ViewSource) string {
	return dt.buildPlainUnion(viewName, sources)
}
