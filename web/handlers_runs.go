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
	"github.com/rs/zerolog/log"
)

// GetRunHistory returns paginated run history for a subscription.
func GetRunHistory(c *fiber.Ctx) error {
	id := c.Params("id")
	limit := c.QueryInt("limit", 25)
	offset := c.QueryInt("offset", 0)
	myLibrary := getLibrary(c)

	entries, total, err := myLibrary.RunHistory(c.UserContext(), id, limit, offset)
	if err != nil {
		log.Error().Err(err).Str("ID", id).Msg("could not load run history")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load run history",
		})
	}

	return c.JSON(fiber.Map{
		"data":   entries,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GetRunSparkline returns daily aggregated observation counts for sparkline display.
func GetRunSparkline(c *fiber.Ctx) error {
	id := c.Params("id")
	myLibrary := getLibrary(c)

	sparkline, err := myLibrary.RunHistorySparkline(c.UserContext(), id)
	if err != nil {
		log.Error().Err(err).Str("ID", id).Msg("could not load sparkline data")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load sparkline data",
		})
	}

	return c.JSON(sparkline)
}
