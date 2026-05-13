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
package figi

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("Enrich", func() {
	It("mints a synthetic FIGI for a delisted predecessor with a known CIK", func() {
		// Blockbuster (NYSE BBI, ~1999-2010). OpenFIGI no longer maps
		// BBI to Blockbuster — the ticker has been recycled to
		// Brickell Biotech (later Fresh Tracks Therapeutics) under a
		// different CIK. The historical walk surfaces Blockbuster with
		// composite_figi="" and the correct CIK; once DelistingDate is
		// known, Enrich's synthetic-from-CIK path mints a stable PVG
		// FIGI that uniquely identifies the predecessor.
		blockbuster := &data.Asset{
			Ticker:        "BBI",
			CIK:           "0001085734",
			Name:          "BLOCKBUSTER INC CL-A",
			DelistingDate: "2010-09-23",
		}

		Enrich(context.Background(), blockbuster)

		Expect(blockbuster.CompositeFigi).To(HavePrefix("PVG"))
		Expect(blockbuster.CompositeFigi).To(HaveLen(12))
		Expect(blockbuster.CompositeFigi).To(Equal(GenerateSyntheticFIGIFromCIK("0001085734", "BBI")))
	})

	It("disambiguates predecessor and successor under a recycled ticker", func() {
		// Same ticker BBI; different CIKs (Blockbuster vs Brickell)
		// must produce different synthetic FIGIs so they coexist as
		// separate asset rows.
		blockbuster := &data.Asset{
			Ticker:        "BBI",
			CIK:           "0001085734",
			DelistingDate: "2010-09-23",
		}
		brickell := &data.Asset{
			Ticker:        "BBI",
			CIK:           "0000819050",
			DelistingDate: "2022-09-08",
		}

		Enrich(context.Background(), blockbuster, brickell)

		Expect(blockbuster.CompositeFigi).NotTo(Equal(brickell.CompositeFigi))
		Expect(blockbuster.CompositeFigi).To(HavePrefix("PVG"))
		Expect(brickell.CompositeFigi).To(HavePrefix("PVG"))
	})

	It("falls back to ticker+name synthesis when CIK is empty", func() {
		// Predecessors that pre-date EDGAR (early 1990s and before)
		// may have no CIK on record. Name is the next-best identifier.
		asset := &data.Asset{
			Ticker:        "OLDTKR",
			Name:          "Old Company Inc",
			DelistingDate: "1995-01-01",
		}

		Enrich(context.Background(), asset)

		Expect(asset.CompositeFigi).To(HavePrefix("PVG"))
		Expect(asset.CompositeFigi).To(Equal(GenerateSyntheticFIGI("OLDTKR", "Old Company Inc")))
	})

	It("leaves a delisted asset without a CIK or name with an empty FIGI", func() {
		// Nothing to disambiguate on; emit a warning (caller drops).
		asset := &data.Asset{
			Ticker:        "ANON",
			DelistingDate: "1990-01-01",
		}

		Enrich(context.Background(), asset)

		Expect(asset.CompositeFigi).To(Equal(""))
	})
})
