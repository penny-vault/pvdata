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
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// QualityIssue represents a single row from data_quality_issues.
type QualityIssue struct {
	ID             uuid.UUID  `json:"id"`
	CheckName      string     `json:"check_name"`
	Severity       string     `json:"severity"`
	DataType       string     `json:"data_type"`
	Ticker         *string    `json:"ticker"`
	CompositeFIGI  *string    `json:"composite_figi"`
	Dimension      *string    `json:"dimension"`
	EventDate      *time.Time `json:"event_date"`
	Field          *string    `json:"field"`
	Message        string     `json:"message"`
	Expected       *string    `json:"expected"`
	Actual         *string    `json:"actual"`
	SubscriptionID *uuid.UUID `json:"subscription_id"`
	RunID          *uuid.UUID `json:"run_id"`
	DetectedAt     time.Time  `json:"detected_at"`
}

// QualityIssuesResponse is the paginated response for GetQualityIssues.
type QualityIssuesResponse struct {
	Issues []QualityIssue `json:"issues"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// QualitySummaryRow represents a single group in the summary query.
type QualitySummaryRow struct {
	CheckName string `json:"check_name"`
	Severity  string `json:"severity"`
	DataType  string `json:"data_type"`
	Count     int    `json:"count"`
}

// GetQualityIssues returns paginated data quality issues with optional filters.
func GetQualityIssues(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)
	severity := c.Query("severity")
	dataType := c.Query("data_type")
	checkName := c.Query("check_name")
	ticker := c.Query("ticker")

	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	// Build dynamic WHERE clause.
	args := []any{}
	argIdx := 1
	where := ""

	if severity != "" {
		where += fmt.Sprintf(" AND severity = $%d", argIdx)

		args = append(args, severity)
		argIdx++
	}

	if dataType != "" {
		where += fmt.Sprintf(" AND data_type = $%d", argIdx)

		args = append(args, dataType)
		argIdx++
	}

	if checkName != "" {
		where += fmt.Sprintf(" AND check_name = $%d", argIdx)

		args = append(args, checkName)
		argIdx++
	}

	if ticker != "" {
		where += fmt.Sprintf(" AND ticker = $%d", argIdx)

		args = append(args, ticker)
		argIdx++
	}

	baseWhere := "WHERE 1=1" + where

	// Count total matching rows.
	countSQL := "SELECT COUNT(*) FROM data_quality_issues " + baseWhere

	var total int
	if err := conn.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		log.Error().Err(err).Msg("could not count quality issues")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not query quality issues",
		})
	}

	// Fetch paginated rows.
	dataSQL := fmt.Sprintf(
		`SELECT id, check_name, severity, data_type, ticker, composite_figi,
		        dimension, event_date, field, message, expected, actual,
		        subscription_id, run_id, detected_at
		 FROM data_quality_issues
		 %s
		 ORDER BY detected_at DESC
		 LIMIT $%d OFFSET $%d`,
		baseWhere, argIdx, argIdx+1,
	)

	args = append(args, limit, offset)

	rows, err := conn.Query(ctx, dataSQL, args...)
	if err != nil {
		log.Error().Err(err).Msg("could not query quality issues")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not query quality issues",
		})
	}

	defer rows.Close()

	issues := make([]QualityIssue, 0, limit)

	for rows.Next() {
		var issue QualityIssue

		if err := rows.Scan(
			&issue.ID,
			&issue.CheckName,
			&issue.Severity,
			&issue.DataType,
			&issue.Ticker,
			&issue.CompositeFIGI,
			&issue.Dimension,
			&issue.EventDate,
			&issue.Field,
			&issue.Message,
			&issue.Expected,
			&issue.Actual,
			&issue.SubscriptionID,
			&issue.RunID,
			&issue.DetectedAt,
		); err != nil {
			log.Error().Err(err).Msg("could not scan quality issue row")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not read quality issues",
			})
		}

		issues = append(issues, issue)
	}

	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("error iterating quality issue rows")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not read quality issues",
		})
	}

	return c.JSON(QualityIssuesResponse{
		Issues: issues,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// GetQualitySummary returns counts of issues grouped by check_name, severity, and data_type.
func GetQualitySummary(c *fiber.Ctx) error {
	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	const summarySQL = `
		SELECT check_name, severity, data_type, COUNT(*) AS count
		FROM data_quality_issues
		GROUP BY check_name, severity, data_type
		ORDER BY severity, check_name, data_type
	`

	rows, err := conn.Query(ctx, summarySQL)
	if err != nil {
		log.Error().Err(err).Msg("could not query quality summary")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not query quality summary",
		})
	}

	defer rows.Close()

	summary := make([]QualitySummaryRow, 0)

	for rows.Next() {
		var row QualitySummaryRow

		if err := rows.Scan(&row.CheckName, &row.Severity, &row.DataType, &row.Count); err != nil {
			log.Error().Err(err).Msg("could not scan quality summary row")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not read quality summary",
			})
		}

		summary = append(summary, row)
	}

	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("error iterating quality summary rows")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not read quality summary",
		})
	}

	return c.JSON(summary)
}
