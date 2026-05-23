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
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/gosimple/slug"
	"github.com/penny-vault/pvdata/library"
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

// GetRunLog returns a paged slice of the captured log text for a specific
// run_history row, with chronological lines plus total/start_line/limit
// metadata. Query parameters: limit (default 1000, capped at 5000) and
// before (upper-bound line cursor, exclusive; omit or 0 for the newest tail).
// `lines` is empty when no log was captured or the 30-day retention has
// cleared it; for the full log unpaged use the /log/download endpoint.
func GetRunLog(c *fiber.Ctx) error {
	id := c.Params("id")
	runID := c.Params("runID")
	myLibrary := getLibrary(c)

	if _, err := myLibrary.SubscriptionFromID(c.UserContext(), id); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	page, err := myLibrary.RunHistoryLogPageFor(c.UserContext(), library.RunHistoryLogPageQuery{
		RunID:  runID,
		Before: c.QueryInt("before", 0),
		Limit:  c.QueryInt("limit", 0),
	})
	if err != nil {
		log.Error().Err(err).Str("runID", runID).Msg("could not load run log")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load run log",
		})
	}

	return c.JSON(fiber.Map{
		"lines":      page.Lines,
		"total":      page.Total,
		"start_line": page.StartLine,
		"limit":      len(page.Lines),
	})
}

// DownloadRunLog returns the captured log text for a run as an attachment.
// The body is raw newline-delimited JSON (the underlying zerolog format).
// Returns 404 when the subscription is unknown, 204 when no log was captured
// or it has been swept by the 30-day retention.
func DownloadRunLog(c *fiber.Ctx) error {
	id := c.Params("id")
	runID := c.Params("runID")
	myLibrary := getLibrary(c)

	sub, err := myLibrary.SubscriptionFromID(c.UserContext(), id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	runLog, err := myLibrary.RunHistoryLog(c.UserContext(), runID)
	if err != nil {
		log.Error().Err(err).Str("runID", runID).Msg("could not load run log")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load run log",
		})
	}

	if runLog == "" {
		return c.SendStatus(fiber.StatusNoContent)
	}

	safeName := slug.Make(sub.Name)
	if safeName == "" {
		safeName = "run"
	}

	filename := fmt.Sprintf("%s-%s.ndjson", safeName, runID)

	c.Set(fiber.HeaderContentType, "application/x-ndjson")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, filename))

	return c.SendString(runLog)
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
