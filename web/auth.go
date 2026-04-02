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
	"context"
	"fmt"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// NewAuthMiddleware returns a Fiber handler that validates Zitadel JWTs.
// When auth.domain is empty, it returns a pass-through middleware for dev mode.
func NewAuthMiddleware() fiber.Handler {
	domain := viper.GetString("auth.domain")
	clientID := viper.GetString("auth.client_id")

	if domain == "" {
		log.Warn().Msg("auth.domain not configured, authentication disabled")

		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}

	jwksURL := fmt.Sprintf("https://%s/oauth/v2/keys", domain)

	k, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		log.Fatal().Err(err).Str("JwksURL", jwksURL).Msg("failed to create JWKS keyfunc")
	}

	issuer := fmt.Sprintf("https://%s", domain)

	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "missing authorization header",
			})
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "invalid authorization header format",
			})
		}

		tokenString := parts[1]

		parserOpts := []jwt.ParserOption{
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithIssuer(issuer),
		}

		if clientID != "" {
			parserOpts = append(parserOpts, jwt.WithAudience(clientID))
		}

		token, err := jwt.Parse(tokenString, k.KeyfuncCtx(context.Background()), parserOpts...)
		if err != nil {
			log.Debug().Err(err).Msg("JWT validation failed")

			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "invalid or expired token",
			})
		}

		if !token.Valid {
			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "invalid token",
			})
		}

		return c.Next()
	}
}
