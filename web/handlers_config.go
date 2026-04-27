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
	"github.com/spf13/viper"
)

// PublicConfig is the runtime config the embedded SPA fetches at boot
// to wire its OIDC client. Only values safe to expose to a browser
// belong here — never secrets.
type PublicConfig struct {
	AuthIssuer   string `json:"auth_issuer"`
	AuthClientID string `json:"auth_client_id"`
	AuthAudience string `json:"auth_audience"`
}

// GetPublicConfig returns the runtime config payload consumed by the SPA.
func GetPublicConfig(c *fiber.Ctx) error {
	return c.JSON(PublicConfig{
		AuthIssuer:   viper.GetString("auth.issuer"),
		AuthClientID: viper.GetString("auth.client_id"),
		AuthAudience: viper.GetString("auth.audience"),
	})
}
