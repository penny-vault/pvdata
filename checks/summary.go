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
package checks

import "fmt"

// FormatSummaryLine returns a one-line summary like:
// "Data quality: 2 critical, 5 errors, 12 warnings"
// Returns empty string if no results.
func FormatSummaryLine(results []CheckResult) string {
	if len(results) == 0 {
		return ""
	}

	counts := map[CheckSeverity]int{}
	for _, r := range results {
		counts[r.Severity]++
	}

	return fmt.Sprintf("Data quality: %d critical, %d errors, %d warnings (run `pvdata check` for details)",
		counts[SeverityCritical],
		counts[SeverityError],
		counts[SeverityWarning],
	)
}
