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
package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/penny-vault/pvdata/provider"
)

type providerDatasetResponse struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	DataTypes   []string `json:"data_types"`
}

// ProviderResponse describes a single provider and its datasets.
type ProviderResponse struct {
	Name              string                             `json:"name"`
	Description       string                             `json:"description"`
	ConfigDescription map[string]string                  `json:"config_description"`
	Datasets          map[string]providerDatasetResponse `json:"datasets"`
}

// GetProviders returns a map of all registered providers keyed by provider key.
func GetProviders(c *fiber.Ctx) error {
	result := make(map[string]ProviderResponse, len(provider.Map))

	for name, p := range provider.Map {
		datasets := p.Datasets()
		datasetMap := make(map[string]providerDatasetResponse, len(datasets))

		for dsKey, ds := range datasets {
			dtNames := make([]string, 0, len(ds.DataTypes))
			for _, dt := range ds.DataTypes {
				dtNames = append(dtNames, dt.Name)
			}

			datasetMap[dsKey] = providerDatasetResponse{
				Name:        ds.Name,
				Description: ds.Description,
				DataTypes:   dtNames,
			}
		}

		result[name] = ProviderResponse{
			Name:              name,
			Description:       p.Description(),
			ConfigDescription: p.ConfigDescription(),
			Datasets:          datasetMap,
		}
	}

	return c.JSON(result)
}
