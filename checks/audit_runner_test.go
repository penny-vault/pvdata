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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/checks"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type stubAuditCheck struct {
	results []checks.CheckResult
}

func (s *stubAuditCheck) Name() string                  { return "stub-audit" }
func (s *stubAuditCheck) Description() string           { return "stub" }
func (s *stubAuditCheck) Phase() checks.CheckPhase      { return checks.PhaseAudit }
func (s *stubAuditCheck) Severity() checks.CheckSeverity { return checks.SeverityWarning }
func (s *stubAuditCheck) DataTypes() []string           { return []string{"fundamental"} }
func (s *stubAuditCheck) Audit(_ context.Context, _ *pgxpool.Pool, _ string, _ *time.Time, _ *time.Duration) ([]checks.CheckResult, error) {
	return s.results, nil
}

var _ = Describe("AuditRunner", func() {
	It("collects results from all audit checks", func() {
		stubResults := []checks.CheckResult{
			{CheckName: "stub-audit", Message: "test issue"},
		}

		runner := checks.NewAuditRunner([]checks.AuditCheck{
			&stubAuditCheck{results: stubResults},
		}, nil)

		opts := checks.AuditOptions{}

		results, err := runner.Run(context.Background(), opts, "test_table")
		Expect(err).ToNot(HaveOccurred())
		Expect(results).To(HaveLen(1))
		Expect(results[0].CheckName).To(Equal("stub-audit"))
	})

	It("returns empty results when no checks registered", func() {
		runner := checks.NewAuditRunner(nil, nil)

		results, err := runner.Run(context.Background(), checks.AuditOptions{}, "test_table")
		Expect(err).ToNot(HaveOccurred())
		Expect(results).To(BeEmpty())
	})
})
