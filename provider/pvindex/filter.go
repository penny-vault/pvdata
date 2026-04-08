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
package pvindex

import (
	"strings"
)

// lpSuffixes is the list of legal-form suffixes that identify a limited partnership.
// Matched as case-insensitive whole-word suffixes against the trimmed asset name.
var lpSuffixes = []string{
	" LP",
	" L.P.",
	" L P",
	" LLP",
	" LIMITED PARTNERSHIP",
}

// isLPName returns true if the asset name ends in a recognized LP suffix.
// Suffix matching is case-insensitive and whitespace-tolerant.
func isLPName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}

	upper := strings.ToUpper(trimmed)
	for _, suffix := range lpSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}

	return false
}
