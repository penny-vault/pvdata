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
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parquet-go/parquet-go"
	"github.com/rs/zerolog/log"
)

// SQLRequest is the JSON body for SQL execution endpoints.
type SQLRequest struct {
	Query string `json:"query"`
}

type queryResult struct {
	Columns   []string `json:"columns"`
	Data      [][]any  `json:"data"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
}

const maxQueryRows = 10000

// ExecuteSQL runs a user-provided SQL query in a read-only transaction.
func ExecuteSQL(c *fiber.Ctx) error {
	var req SQLRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid request body",
		})
	}

	if req.Query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "query is required",
		})
	}

	myLibrary := getLibrary(c)

	result, err := executeReadOnlyQuery(c.UserContext(), myLibrary.Pool, req.Query)
	if err != nil {
		log.Error().Err(err).Msg("SQL query execution failed")

		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: err.Error(),
		})
	}

	return c.JSON(result)
}

// ExportSQL runs a SQL query and exports results as CSV or Parquet.
func ExportSQL(c *fiber.Ctx) error {
	var req SQLRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid request body",
		})
	}

	if req.Query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "query is required",
		})
	}

	format := c.Query("format", "csv")
	myLibrary := getLibrary(c)

	result, err := executeReadOnlyQuery(c.UserContext(), myLibrary.Pool, req.Query)
	if err != nil {
		log.Error().Err(err).Msg("SQL export query execution failed")

		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: err.Error(),
		})
	}

	switch format {
	case "parquet":
		return exportParquet(c, result)
	default:
		return exportCSV(c, result)
	}
}

// exportCSV writes the query result as a CSV file response.
func exportCSV(c *fiber.Ctx, result *queryResult) error {
	var buf bytes.Buffer

	w := csv.NewWriter(&buf)

	// Write header
	if err := w.Write(result.Columns); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not write CSV header",
		})
	}

	// Write data rows
	for _, row := range result.Data {
		record := make([]string, len(row))
		for i, val := range row {
			record[i] = fmt.Sprintf("%v", val)
		}

		if err := w.Write(record); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not write CSV row",
			})
		}
	}

	w.Flush()

	if err := w.Error(); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "CSV write error",
		})
	}

	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", "attachment; filename=export.csv")

	return c.Send(buf.Bytes())
}

// exportParquet writes the query result as a Parquet file response.
func exportParquet(c *fiber.Ctx, result *queryResult) error {
	var buf bytes.Buffer

	// Build schema with all-string columns using parquet.Group
	group := make(parquet.Group, len(result.Columns))
	for _, col := range result.Columns {
		group[col] = parquet.String()
	}

	schema := parquet.NewSchema("export", group)

	writer := parquet.NewWriter(&buf, schema)

	for _, row := range result.Data {
		pqRow := make(parquet.Row, len(row))
		for i, val := range row {
			pqRow[i] = parquet.ValueOf(fmt.Sprintf("%v", val))
		}

		if _, err := writer.WriteRows([]parquet.Row{pqRow}); err != nil {
			log.Error().Err(err).Msg("could not write parquet row")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not write parquet data",
			})
		}
	}

	if err := writer.Close(); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not finalize parquet file",
		})
	}

	c.Set("Content-Type", "application/octet-stream")
	c.Set("Content-Disposition", "attachment; filename=export.parquet")

	return c.Send(buf.Bytes())
}

// executeReadOnlyQuery executes a SQL query inside a read-only transaction
// with a 30-second timeout.
func executeReadOnlyQuery(ctx context.Context, pool *pgxpool.Pool, query string) (*queryResult, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tx, err := pool.BeginTx(queryCtx, pgx.TxOptions{
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("could not begin transaction: %w", err)
	}

	defer func() {
		_ = tx.Rollback(queryCtx)
	}()

	rows, err := tx.Query(queryCtx, query)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	columns := make([]string, 0, len(rows.FieldDescriptions()))
	for _, fd := range rows.FieldDescriptions() {
		columns = append(columns, string(fd.Name))
	}

	data := make([][]any, 0)
	truncated := false

	for rows.Next() {
		if len(data) >= maxQueryRows {
			truncated = true

			break
		}

		values, vErr := rows.Values()
		if vErr != nil {
			return nil, fmt.Errorf("could not scan row: %w", vErr)
		}

		data = append(data, values)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return &queryResult{
		Columns:   columns,
		Data:      data,
		Count:     len(data),
		Truncated: truncated,
	}, nil
}
