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

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type stubInlineCheck struct {
	name     string
	severity checks.CheckSeverity
}

func (s *stubInlineCheck) Name() string                   { return s.name }
func (s *stubInlineCheck) Description() string            { return "stub check" }
func (s *stubInlineCheck) Phase() checks.CheckPhase       { return checks.PhaseInline }
func (s *stubInlineCheck) Severity() checks.CheckSeverity { return s.severity }
func (s *stubInlineCheck) DataTypes() []string            { return []string{"fundamental"} }
func (s *stubInlineCheck) Validate(_ context.Context, _ *data.Observation) ([]checks.CheckResult, bool) {
	return nil, false
}

var _ = Describe("Registry", func() {
	BeforeEach(func() {
		checks.ClearRegistry()
	})

	It("registers and retrieves inline checks", func() {
		stub := &stubInlineCheck{name: "test-check", severity: checks.SeverityWarning}
		checks.RegisterInline(stub)
		Expect(checks.InlineChecks()).To(HaveLen(1))
		Expect(checks.InlineChecks()[0].Name()).To(Equal("test-check"))
	})

	It("returns empty slices when no checks registered", func() {
		Expect(checks.InlineChecks()).To(BeEmpty())
		Expect(checks.AuditChecks()).To(BeEmpty())
	})
})
