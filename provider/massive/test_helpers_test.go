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
package massive

import (
	"time"
)

// d parses a YYYY-MM-DD string into a UTC time.Time; tests panic on
// malformed input so a typo surfaces immediately rather than producing
// a silent zero value.
func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}

	return t
}

// buildBBIArchive returns an EODArchive carrying Blockbuster's BBI
// lifecycle range [2003-09-10 .. 2010-07-06]. Used by the
// shortDelistedNoCoverageReason tests as a representative archive
// covering one ticker with one closed lifecycle.
func buildBBIArchive() *EODArchive {
	archive := newEmptyArchive()
	archive.tickers["BBI"] = []dateRange{
		{Start: d("2003-09-10"), End: d("2010-07-06")},
	}
	archive.coverageStart = d("2003-09-10")
	archive.coverageEnd = d("2010-07-06")

	return archive
}

// newFetcherWithArchive returns a massiveAssetFetcher with the given
// archive pre-installed and the lazy-init sync.Once already fired so
// eodArchiveForRun returns the supplied archive without trying to
// load anything from disk.
func newFetcherWithArchive(archive *EODArchive) *massiveAssetFetcher {
	api := &massiveAssetFetcher{
		eodArchive: archive,
	}

	api.eodArchiveOnce.Do(func() {})

	return api
}
