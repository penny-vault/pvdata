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
package data_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

type recordingFiler struct {
	urls map[string]string
}

func (r *recordingFiler) CreateFile(name string, _ []byte) (string, error) {
	url := "https://example.test/" + name
	if r.urls == nil {
		r.urls = map[string]string{}
	}
	r.urls[name] = url

	return url, nil
}

var _ = Describe("Asset.SaveFiles", func() {
	It("populates IconUrl with the returned URL", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			Icon:          []byte{1, 2, 3},
			IconMimeType:  "image/png",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.IconUrl).To(Equal("https://example.test/BBG000FOO-icon.png"))
	})

	It("populates LogoUrl with the returned URL", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			Logo:          []byte{4, 5, 6},
			LogoMimeType:  "image/jpeg",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.LogoUrl).To(Equal("https://example.test/BBG000FOO-logo.jpg"))
	})

	It("leaves URL unchanged when there is no payload", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			IconUrl:       "previous-url",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.IconUrl).To(Equal("previous-url"))
	})
})
