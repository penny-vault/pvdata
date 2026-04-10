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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
)

const qualityCheckKey = "__quality_check__"

// TriggerQualityCheck starts a data quality audit in the background.
func TriggerQualityCheck(c *fiber.Ctx) error {
	myLibrary := getLibrary(c)
	registry := getRegistry(c)

	if _, ok := registry.Load(qualityCheckKey); ok {
		return c.Status(fiber.StatusConflict).JSON(HttpError{
			Code:    "409",
			Message: "a quality check is already in progress",
		})
	}

	run := &activeRun{
		events: make(chan sseEvent, 1000),
		done:   make(chan struct{}),
	}
	registry.Store(qualityCheckKey, run)

	go executeQualityCheck(myLibrary, run, registry)

	return c.JSON(fiber.Map{"status": "started"})
}

// QualityCheckEvents streams SSE for an active quality check.
func QualityCheckEvents(c *fiber.Ctx) error {
	registry := getRegistry(c)

	run, ok := registry.Load(qualityCheckKey)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "no active quality check",
		})
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		for {
			select {
			case evt, ok := <-run.events:
				if !ok {
					return
				}

				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data)

				if err := w.Flush(); err != nil {
					return
				}
			case <-run.done:
				for {
					select {
					case evt, ok := <-run.events:
						if !ok {
							return
						}

						fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data)
						w.Flush()
					default:
						return
					}
				}
			}
		}
	})

	return nil
}

func executeQualityCheck(myLibrary *library.Library, run *activeRun, registry *RunRegistry) {
	defer func() {
		close(run.done)
		time.Sleep(5 * time.Second)
		registry.Delete(qualityCheckKey)
		close(run.events)
	}()

	ctx := context.Background()

	subscriptions, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not load subscriptions for quality check")
		emitQualityEvent(run, "failed", map[string]interface{}{"error": err.Error()})

		return
	}

	runner := checks.NewAuditRunner(checks.AuditChecks(), myLibrary.Pool)
	runID := uuid.New()

	var totalIssues int

	emitQualityEvent(run, "started", map[string]string{"status": "running"})

	for _, sub := range subscriptions {
		if !sub.Active {
			continue
		}

		for idx, dt := range sub.DataTypes {
			table := sub.DataTables[idx]

			opts := checks.AuditOptions{
				DataTypes: []string{dt},
			}

			emitQualityEvent(run, "checking", map[string]string{
				"subscription": sub.Name,
				"data_type":    dt,
				"table":        table,
			})

			results, err := runner.Run(ctx, opts, table)
			if err != nil {
				log.Error().Err(err).Str("table", table).Msg("audit check failed")
				emitQualityEvent(run, "check_error", map[string]string{
					"subscription": sub.Name,
					"table":        table,
					"error":        err.Error(),
				})

				continue
			}

			for i := range results {
				if results[i].DataType == "" {
					results[i].DataType = dt
				}
			}

			if len(results) > 0 {
				if saveErr := checks.SaveResults(ctx, myLibrary.Pool, results, sub.ID, runID); saveErr != nil {
					log.Error().Err(saveErr).Msg("failed to save check results")
				}
			}

			totalIssues += len(results)

			emitQualityEvent(run, "checked", map[string]interface{}{
				"subscription": sub.Name,
				"data_type":    dt,
				"issues":       len(results),
				"total_issues": totalIssues,
			})
		}
	}

	emitQualityEvent(run, "completed", map[string]interface{}{
		"total_issues": totalIssues,
	})
}

func emitQualityEvent(run *activeRun, event string, payload interface{}) {
	d, _ := json.Marshal(payload)
	run.events <- sseEvent{Event: event, Data: string(d)}
}
