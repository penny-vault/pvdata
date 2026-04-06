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
package checks_test

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type passingCheck struct{}

func (p *passingCheck) Name() string                   { return "passing" }
func (p *passingCheck) Description() string            { return "always passes" }
func (p *passingCheck) Phase() checks.CheckPhase       { return checks.PhaseInline }
func (p *passingCheck) Severity() checks.CheckSeverity { return checks.SeverityWarning }
func (p *passingCheck) DataTypes() []string            { return []string{"fundamental"} }
func (p *passingCheck) Validate(_ context.Context, _ *data.Observation) ([]checks.CheckResult, bool) {
	return nil, false
}

type failingCheck struct {
	block bool
}

func (f *failingCheck) Name() string                   { return "failing" }
func (f *failingCheck) Description() string            { return "always fails" }
func (f *failingCheck) Phase() checks.CheckPhase       { return checks.PhaseInline }
func (f *failingCheck) Severity() checks.CheckSeverity { return checks.SeverityCritical }
func (f *failingCheck) DataTypes() []string            { return []string{"fundamental"} }
func (f *failingCheck) Validate(_ context.Context, obs *data.Observation) ([]checks.CheckResult, bool) {
	return []checks.CheckResult{
		{
			CheckName: "failing",
			Severity:  checks.SeverityCritical,
			Ticker:    obs.Fundamental.Ticker,
			DataType:  "fundamental",
			Message:   "test failure",
		},
	}, f.block
}

var _ = Describe("InlineValidator", func() {
	It("returns no results when all checks pass", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{&passingCheck{}})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("returns results and block=true when a blocking check fails", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{&failingCheck{block: true}})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].CheckName).To(Equal("failing"))
		Expect(block).To(BeTrue())
	})

	It("returns results but block=false when a non-blocking check fails", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{&failingCheck{block: false}})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(block).To(BeFalse())
	})

	It("aggregates results from multiple checks", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{
			&failingCheck{block: false},
			&failingCheck{block: true},
		})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(2))
		Expect(block).To(BeTrue())
	})
})
