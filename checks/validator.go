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

import (
	"context"

	"github.com/penny-vault/pvdata/data"
)

type InlineValidator struct {
	checks []InlineCheck
}

func NewInlineValidator(checks []InlineCheck) *InlineValidator {
	return &InlineValidator{checks: checks}
}

func (v *InlineValidator) Validate(ctx context.Context, obs *data.Observation) ([]CheckResult, bool) {
	var results []CheckResult

	block := false

	for _, c := range v.checks {
		r, b := c.Validate(ctx, obs)
		results = append(results, r...)

		if b {
			block = true
		}
	}

	return results, block
}
