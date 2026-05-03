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
package ishares

import (
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/rs/zerolog"
)

var _ = Describe("mintAndEmitISharesNew", func() {
	var (
		figiMap       map[string]string
		preRunFIGIs   map[string]struct{}
		emittedAssets map[string]struct{}
		assetMetadata map[string]*data.Asset
		obsTemplate   *data.Observation
		logger        zerolog.Logger
		out           chan *data.Observation
		eventDate     time.Time
		drainObserver func() []*data.Observation
		alreadyKnownF = "BBG000B9XRY4" // pretend pre-existing AAPL FIGI
	)

	BeforeEach(func() {
		figiMap = map[string]string{}
		preRunFIGIs = map[string]struct{}{}
		emittedAssets = map[string]struct{}{}
		assetMetadata = map[string]*data.Asset{}
		obsTemplate = &data.Observation{
			ObservationDate:  time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
			SubscriptionID:   uuid.New(),
			SubscriptionName: "iShares Index Constituents",
		}
		logger = zerolog.Nop()
		out = make(chan *data.Observation, 256)
		eventDate = time.Date(2006, 9, 29, 0, 0, 0, 0, time.UTC)

		drainObserver = func() []*data.Observation {
			close(out)
			obs := make([]*data.Observation, 0)
			for o := range out {
				obs = append(obs, o)
			}
			return obs
		}
	})

	Context("when a holding's FIGI is in the pre-run set (already in DB)", func() {
		It("emits no asset and no EOD", func() {
			figiMap["AAPL"] = alreadyKnownF
			preRunFIGIs[alreadyKnownF] = struct{}{}

			holdings := []iSharesHolding{
				{Ticker: "AAPL", Name: "APPLE INC", Exchange: "NASDAQ", Weight: 0.05, Price: 250.00},
			}

			n := mintAndEmitISharesNew(holdings, eventDate, "RUA", figiMap, preRunFIGIs, emittedAssets, assetMetadata, obsTemplate, &logger, out)
			Expect(n).To(BeZero())

			obs := drainObserver()
			Expect(obs).To(BeEmpty())
		})
	})

	Context("when a holding has been newly resolved by OpenFIGI to a real Bloomberg composite", func() {
		It("emits asset and EOD using that Bloomberg FIGI", func() {
			figiMap["NEW"] = "BBG000NEW1234"
			assetMetadata["NEW"] = &data.Asset{Ticker: "NEW", Name: "NEW CORP", CompositeFigi: "BBG000NEW1234", AssetType: data.CommonStock}

			holdings := []iSharesHolding{
				{Ticker: "NEW", Name: "NEW CORP", Exchange: "NYSE", Weight: 0.01, Price: 42.50},
			}

			n := mintAndEmitISharesNew(holdings, eventDate, "RUA", figiMap, preRunFIGIs, emittedAssets, assetMetadata, obsTemplate, &logger, out)
			Expect(n).To(Equal(2))

			obs := drainObserver()
			Expect(obs).To(HaveLen(2))

			var assetObs, eodObs *data.Observation
			for _, o := range obs {
				if o.AssetObject != nil {
					assetObs = o
				}
				if o.EodQuote != nil {
					eodObs = o
				}
			}

			Expect(assetObs).ToNot(BeNil())
			Expect(assetObs.AssetObject.Ticker).To(Equal("NEW"))
			Expect(assetObs.AssetObject.CompositeFigi).To(Equal("BBG000NEW1234"))
			Expect(assetObs.AssetObject.Name).To(Equal("NEW CORP"))
			Expect(assetObs.AssetObject.PrimaryExchange).To(Equal(data.NYSEExchange))
			Expect(assetObs.AssetObject.AssetType).To(Equal(data.CommonStock))

			Expect(eodObs).ToNot(BeNil())
			Expect(eodObs.EodQuote.Ticker).To(Equal("NEW"))
			Expect(eodObs.EodQuote.CompositeFigi).To(Equal("BBG000NEW1234"))
			Expect(eodObs.EodQuote.Date).To(Equal(eventDate))
			Expect(eodObs.EodQuote.Close).To(Equal(42.50))
			Expect(eodObs.EodQuote.Open).To(Equal(42.50))
			Expect(eodObs.EodQuote.High).To(Equal(42.50))
			Expect(eodObs.EodQuote.Low).To(Equal(42.50))
			Expect(eodObs.EodQuote.Split).To(Equal(1.0))
		})
	})

	Context("when a holding remains unresolved after OpenFIGI", func() {
		It("mints a synthetic PV FIGI and emits asset + EOD", func() {
			holdings := []iSharesHolding{
				{Ticker: "KWD", Name: "KELLWOOD CO.", Exchange: "NYSE", Weight: 0.0001, Price: 28.83},
			}

			n := mintAndEmitISharesNew(holdings, eventDate, "RUA", figiMap, preRunFIGIs, emittedAssets, assetMetadata, obsTemplate, &logger, out)
			Expect(n).To(Equal(2))

			obs := drainObserver()
			Expect(obs).To(HaveLen(2))

			synth := figiMap["KWD"]
			Expect(synth).ToNot(BeEmpty())
			Expect(figi.IsSyntheticFIGI(synth)).To(BeTrue())
			Expect(figi.ValidateFIGICheckDigit(synth)).To(BeTrue())

			var assetObs, eodObs *data.Observation
			for _, o := range obs {
				if o.AssetObject != nil {
					assetObs = o
				}
				if o.EodQuote != nil {
					eodObs = o
				}
			}

			Expect(assetObs.AssetObject.CompositeFigi).To(Equal(synth))
			Expect(assetObs.AssetObject.Name).To(Equal("KELLWOOD CO."))
			Expect(assetObs.AssetObject.PrimaryExchange).To(Equal(data.NYSEExchange))

			Expect(eodObs.EodQuote.CompositeFigi).To(Equal(synth))
			Expect(eodObs.EodQuote.Close).To(Equal(28.83))
		})

		It("is deterministic across re-runs for the same (ticker, name)", func() {
			s1 := figi.GenerateSyntheticFIGI("KWD", "KELLWOOD CO.")
			s2 := figi.GenerateSyntheticFIGI("KWD", "KELLWOOD CO.")
			Expect(s1).To(Equal(s2))
		})
	})

	Context("when the same ticker appears across multiple invocations", func() {
		It("emits the asset only once but emits EOD per call", func() {
			holdings := []iSharesHolding{
				{Ticker: "KWD", Name: "KELLWOOD CO.", Exchange: "NYSE", Weight: 0.0001, Price: 28.83},
			}

			// First call (e.g. date #1)
			out = make(chan *data.Observation, 256)
			n1 := mintAndEmitISharesNew(holdings, eventDate, "RUA", figiMap, preRunFIGIs, emittedAssets, assetMetadata, obsTemplate, &logger, out)
			close(out)
			obs1 := make([]*data.Observation, 0)
			for o := range out {
				obs1 = append(obs1, o)
			}
			Expect(n1).To(Equal(2))
			Expect(obs1).To(HaveLen(2))

			// Second call (e.g. date #2) — same ticker, different price
			holdings[0].Price = 30.00
			out = make(chan *data.Observation, 256)
			eventDate2 := eventDate.AddDate(0, 0, 1)
			n2 := mintAndEmitISharesNew(holdings, eventDate2, "RUA", figiMap, preRunFIGIs, emittedAssets, assetMetadata, obsTemplate, &logger, out)
			close(out)
			obs2 := make([]*data.Observation, 0)
			for o := range out {
				obs2 = append(obs2, o)
			}
			// Asset already emitted on first call -> only EOD should appear
			Expect(n2).To(Equal(1))
			Expect(obs2).To(HaveLen(1))
			Expect(obs2[0].EodQuote).ToNot(BeNil())
			Expect(obs2[0].EodQuote.Close).To(Equal(30.00))
		})
	})

	Context("when a holding has zero price", func() {
		It("emits the asset but skips the EOD", func() {
			holdings := []iSharesHolding{
				{Ticker: "KWD", Name: "KELLWOOD CO.", Exchange: "NYSE", Weight: 0.0001, Price: 0},
			}

			n := mintAndEmitISharesNew(holdings, eventDate, "RUA", figiMap, preRunFIGIs, emittedAssets, assetMetadata, obsTemplate, &logger, out)
			Expect(n).To(Equal(1))

			obs := drainObserver()
			Expect(obs).To(HaveLen(1))
			Expect(obs[0].AssetObject).ToNot(BeNil())
			Expect(obs[0].EodQuote).To(BeNil())
		})
	})

	Context("when a mix of pre-run, OpenFIGI-discovered, and unresolved holdings appear", func() {
		It("only emits for the latter two", func() {
			figiMap["AAPL"] = alreadyKnownF
			preRunFIGIs[alreadyKnownF] = struct{}{}

			figiMap["NEW"] = "BBG000NEW1234"
			assetMetadata["NEW"] = &data.Asset{Ticker: "NEW", Name: "NEW CORP", CompositeFigi: "BBG000NEW1234"}

			holdings := []iSharesHolding{
				{Ticker: "AAPL", Name: "APPLE INC", Exchange: "NASDAQ", Weight: 0.05, Price: 250.00},
				{Ticker: "NEW", Name: "NEW CORP", Exchange: "NYSE", Weight: 0.01, Price: 42.50},
				{Ticker: "KWD", Name: "KELLWOOD CO.", Exchange: "NYSE", Weight: 0.0001, Price: 28.83},
			}

			n := mintAndEmitISharesNew(holdings, eventDate, "RUA", figiMap, preRunFIGIs, emittedAssets, assetMetadata, obsTemplate, &logger, out)
			// 2 emissions (asset+EOD) for NEW and 2 for KWD = 4
			Expect(n).To(Equal(4))

			obs := drainObserver()
			Expect(obs).To(HaveLen(4))

			tickersWithAsset := map[string]bool{}
			tickersWithEOD := map[string]bool{}
			for _, o := range obs {
				if o.AssetObject != nil {
					tickersWithAsset[o.AssetObject.Ticker] = true
				}
				if o.EodQuote != nil {
					tickersWithEOD[o.EodQuote.Ticker] = true
				}
			}

			Expect(tickersWithAsset).ToNot(HaveKey("AAPL"))
			Expect(tickersWithAsset).To(HaveKey("NEW"))
			Expect(tickersWithAsset).To(HaveKey("KWD"))

			Expect(tickersWithEOD).ToNot(HaveKey("AAPL"))
			Expect(tickersWithEOD).To(HaveKey("NEW"))
			Expect(tickersWithEOD).To(HaveKey("KWD"))
		})
	})
})
