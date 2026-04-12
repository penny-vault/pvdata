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

package provider

import (
	"sort"
	"strings"

	"github.com/adrg/strutil"
	"github.com/adrg/strutil/metrics"
)

const suggestThreshold = 0.85

// SuggestMatch returns up to 3 candidates that are similar to the input
// (Jaro-Winkler score >= 0.85), sorted by descending similarity.
// Returns nil if no candidate meets the threshold.
func SuggestMatch(input string, candidates []string) []string {
	type scored struct {
		value string
		score float64
	}

	inputLower := strings.ToLower(input)
	jw := metrics.NewJaroWinkler()

	var matches []scored

	for _, c := range candidates {
		score := strutil.Similarity(inputLower, strings.ToLower(c), jw)
		if score >= suggestThreshold {
			matches = append(matches, scored{value: c, score: score})
		}
	}

	if len(matches) == 0 {
		return nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	limit := min(3, len(matches))

	result := make([]string, limit)
	for i := range limit {
		result[i] = matches[i].value
	}

	return result
}
