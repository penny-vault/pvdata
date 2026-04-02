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
	"regexp"
	"slices"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"
)

var validTableName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// indexedColumnsForDataType returns the columns that have indexes for a given
// data type, suitable for exact-match filtering.
func indexedColumnsForDataType(datatype string) []string {
	switch datatype {
	case "eod":
		return []string{"ticker", "composite_figi", "event_date"}
	case "fundamental":
		return []string{"ticker", "composite_figi", "event_date", "dimension"}
	case "metric":
		return []string{"ticker", "composite_figi", "event_date"}
	case "rating":
		return []string{"ticker", "composite_figi", "event_date"}
	case "consensus":
		return []string{"ticker", "composite_figi", "event_date"}
	case "estimate":
		return []string{"ticker", "composite_figi", "event_date", "series"}
	case "custom":
		return []string{"ticker", "composite_figi", "event_date", "key"}
	case "quote":
		return []string{"ticker", "composite_figi"}
	case "economic-indicator":
		return []string{"series", "event_date"}
	case "index":
		return []string{"ticker", "index_name", "event_date"}
	case "asset-description":
		return []string{"ticker", "composite_figi", "name"}
	case "market-holidays":
		return []string{"exchange", "event_date"}
	default:
		return []string{}
	}
}

// GetSubscriptionData queries dynamic data from a subscription's data table.
func GetSubscriptionData(c *fiber.Ctx) error {
	id := c.Params("id")
	datatype := c.Params("datatype")

	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	sub, err := myLibrary.SubscriptionFromID(ctx, id)
	if err != nil {
		log.Error().Err(err).Str("ID", id).Msg("subscription not found")

		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	tableName, ok := sub.DataTablesMap[datatype]
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: fmt.Sprintf("data type %q not found for this subscription", datatype),
		})
	}

	// The index data type uses _snapshot and _changelog suffixed tables.
	// Default to browsing the changelog (membership changes over time).
	subtable := c.Query("subtable", "")
	if datatype == "index" {
		if subtable == "snapshot" {
			tableName += "_snapshot"
		} else {
			tableName += "_changelog"
		}
	}

	if !validTableName.MatchString(tableName) {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid table name",
		})
	}

	limit := c.QueryInt("limit", 100)
	offset := c.QueryInt("offset", 0)
	search := c.Query("q")
	sort := c.Query("sort", "")
	order := c.Query("order", "asc")

	// The frontend sends which column to search via search_col param.
	// We validate it against the known indexed columns for this data type.
	indexedCols := indexedColumnsForDataType(datatype)
	searchCol := c.Query("search_col", "")

	if searchCol != "" && !slices.Contains(indexedCols, searchCol) {
		searchCol = ""
	}

	// Build the count query
	countQuery := fmt.Sprintf("SELECT count(*) FROM %s", tableName)
	if search != "" && searchCol != "" {
		countQuery = fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tableName, searchCol)
	}

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}
	defer conn.Release()

	var total int

	if search != "" && searchCol != "" {
		err = conn.QueryRow(ctx, countQuery, search).Scan(&total)
	} else {
		err = conn.QueryRow(ctx, countQuery).Scan(&total)
	}

	if err != nil {
		log.Error().Err(err).Msg("could not count rows")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not count rows",
		})
	}

	// Build the data query
	dataQuery := fmt.Sprintf("SELECT * FROM %s", tableName)

	if search != "" && searchCol != "" {
		dataQuery = fmt.Sprintf("SELECT * FROM %s WHERE %s = $1", tableName, searchCol)
	}

	if sort != "" && validTableName.MatchString(sort) {
		direction := "ASC"
		if order == "desc" {
			direction = "DESC"
		}

		dataQuery += fmt.Sprintf(" ORDER BY %s %s", sort, direction)
	}

	dataQuery += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)

	if search != "" && searchCol != "" {
		r, qErr := conn.Query(ctx, dataQuery, search)
		if qErr != nil {
			log.Error().Err(qErr).Msg("could not query data")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not query data",
			})
		}

		defer r.Close()

		columns := make([]string, 0, len(r.FieldDescriptions()))
		for _, fd := range r.FieldDescriptions() {
			columns = append(columns, string(fd.Name))
		}

		data := make([][]any, 0)

		for r.Next() {
			values, vErr := r.Values()
			if vErr != nil {
				log.Error().Err(vErr).Msg("could not scan row values")

				return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
					Code:    "500",
					Message: "could not scan row values",
				})
			}

			data = append(data, values)
		}

		return c.JSON(fiber.Map{
			"columns":        columns,
			"data":           data,
			"total":          total,
			"limit":          limit,
			"offset":         offset,
			"search_columns": indexedCols,
		})
	}

	r, qErr := conn.Query(ctx, dataQuery)
	if qErr != nil {
		log.Error().Err(qErr).Msg("could not query data")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not query data",
		})
	}

	defer r.Close()

	columns := make([]string, 0, len(r.FieldDescriptions()))
	for _, fd := range r.FieldDescriptions() {
		columns = append(columns, string(fd.Name))
	}

	data := make([][]any, 0)

	for r.Next() {
		values, vErr := r.Values()
		if vErr != nil {
			log.Error().Err(vErr).Msg("could not scan row values")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not scan row values",
			})
		}

		data = append(data, values)
	}

	return c.JSON(fiber.Map{
		"columns":        columns,
		"data":           data,
		"total":          total,
		"limit":          limit,
		"offset":         offset,
		"search_columns": indexedCols,
	})
}
