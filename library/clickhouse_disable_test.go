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
package library_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/spf13/viper"
)

var _ = Describe("ClickHouse disable flag", func() {
	BeforeEach(func() {
		viper.Reset()
	})

	AfterEach(func() {
		viper.Reset()
	})

	Describe("IsClickHouseDisabled", func() {
		It("defaults to false", func() {
			lib := &library.Library{}
			Expect(lib.IsClickHouseDisabled()).To(BeFalse())
		})

		It("returns true when clickhouse.disabled is set", func() {
			viper.Set("clickhouse.disabled", true)

			lib := &library.Library{}
			Expect(lib.IsClickHouseDisabled()).To(BeTrue())
		})
	})

	Describe("ClickHouse", func() {
		It("returns ErrClickHouseDisabled when disabled, without dialing", func() {
			viper.Set("clickhouse.disabled", true)
			// Setting a bogus URL proves the disable check shortcuts
			// before the DSN is ever parsed or dialed.
			viper.Set("clickhouse.url", "clickhouse://nonexistent.invalid:9000/")

			lib := &library.Library{}
			conn, err := lib.ClickHouse(context.Background())
			Expect(conn).To(BeNil())
			Expect(errors.Is(err, library.ErrClickHouseDisabled)).To(BeTrue())
		})
	})

	Describe("HasClickHouseBackedTypes", func() {
		It("is true when the subscription includes a CH-backed type", func() {
			sub := &library.Subscription{DataTypes: []string{data.IntradayKey}}
			Expect(sub.HasClickHouseBackedTypes()).To(BeTrue())
		})

		It("is false for a subscription with only Postgres-backed types", func() {
			sub := &library.Subscription{DataTypes: []string{data.EODKey, data.AssetKey}}
			Expect(sub.HasClickHouseBackedTypes()).To(BeFalse())
		})

		It("is true when at least one of multiple types is CH-backed", func() {
			sub := &library.Subscription{DataTypes: []string{data.AssetKey, data.IntradayKey}}
			Expect(sub.HasClickHouseBackedTypes()).To(BeTrue())
		})
	})
})
