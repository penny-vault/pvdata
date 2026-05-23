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
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// NewAuthMiddleware returns a Fiber handler that validates RS256 JWTs against
// the configured OIDC issuer (auth.issuer, auth.jwks_url, auth.audience).
// When auth.issuer is empty, returns a pass-through middleware for dev mode.
func NewAuthMiddleware() fiber.Handler {
	issuer := viper.GetString("auth.issuer")
	jwksURL := viper.GetString("auth.jwks_url")
	audience := viper.GetString("auth.audience")

	if issuer == "" {
		log.Warn().Msg("auth.issuer not configured, authentication disabled")

		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}

	if jwksURL == "" {
		log.Fatal().Msg("auth.jwks_url is required when auth.issuer is set")
	}

	k, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		log.Fatal().Err(err).Str("JwksURL", jwksURL).Msg("failed to create JWKS keyfunc")
	}

	return func(c *fiber.Ctx) error {
		// Check Authorization header first, then fall back to ?token= query param (for SSE)
		var tokenString string

		authHeader := c.Get("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				tokenString = parts[1]
			}
		}

		if tokenString == "" {
			tokenString = c.Query("token")
		}

		if tokenString == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(HttpError{
				Code:    "401",
				Message: "missing authorization",
			})
		}

		parserOpts := []jwt.ParserOption{
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithIssuer(issuer),
		}

		if audience != "" {
			parserOpts = append(parserOpts, jwt.WithAudience(audience))
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
