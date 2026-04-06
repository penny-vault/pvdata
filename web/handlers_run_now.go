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
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
)

// getRegistry retrieves the RunRegistry from request context.
func getRegistry(c *fiber.Ctx) *RunRegistry {
	return c.Locals("registry").(*RunRegistry)
}

// TriggerRun starts an on-demand run for a subscription.
// Accepts optional query param: ?lookback=30d (e.g. "7d", "30d", "365d"). Default: 14d.
func TriggerRun(c *fiber.Ctx) error {
	id := c.Params("id")
	myLibrary := getLibrary(c)
	registry := getRegistry(c)
	ctx := c.UserContext()

	sub, err := myLibrary.SubscriptionFromID(ctx, id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	subID := sub.ID.String()

	// Reject if already running
	if _, ok := registry.Load(subID); ok {
		return c.Status(fiber.StatusConflict).JSON(HttpError{
			Code:    "409",
			Message: "a run is already in progress for this subscription",
		})
	}

	// Parse lookback duration from query param (e.g. "30d")
	lookback := 14 * 24 * time.Hour // default 14 days

	if lb := c.Query("lookback"); lb != "" {
		parsed, err := parseLookback(lb)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(HttpError{
				Code:    "400",
				Message: "invalid lookback: " + err.Error(),
			})
		}

		lookback = parsed
	}

	// Validate provider and dataset exist
	subProvider, ok := provider.Map[sub.Provider]
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "provider not found: " + sub.Provider,
		})
	}

	if _, ok := subProvider.Datasets()[sub.Dataset]; !ok {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "dataset not found: " + sub.Dataset,
		})
	}

	// Create registry entry with buffered channels
	run := &activeRun{
		events: make(chan sseEvent, 1000),
		done:   make(chan struct{}),
	}
	registry.Store(subID, run)

	// Launch the run in a background goroutine
	go executeRun(myLibrary, sub, run, registry, lookback)

	return c.JSON(fiber.Map{"status": "started"})
}

// parseLookback parses a duration string like "14d", "30d", "365d" into time.Duration.
func parseLookback(s string) (time.Duration, error) {
	if len(s) < 2 || s[len(s)-1] != 'd' {
		return 0, fmt.Errorf("expected format like '14d', got %q", s)
	}

	days := 0

	for _, c := range s[:len(s)-1] {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("expected format like '14d', got %q", s)
		}

		days = days*10 + int(c-'0')
	}

	if days == 0 {
		return 0, fmt.Errorf("lookback must be at least 1d")
	}

	return time.Duration(days) * 24 * time.Hour, nil
}

// executeRun runs the subscription fetch and feeds events into the activeRun channels.
func executeRun(myLibrary *library.Library, sub *library.Subscription, run *activeRun, registry *RunRegistry, lookback time.Duration) {
	subID := sub.ID.String()

	defer func() {
		close(run.done)
		// Grace period so SSE clients can read the final event
		time.Sleep(5 * time.Second)
		registry.Delete(subID)
		close(run.events)
	}()

	ctx := context.Background()

	// Manage partitions and migrations
	if err := sub.ManagePartitions(ctx); err != nil {
		log.Error().Err(err).Msg("ManagePartitions failed during on-demand run")
	}

	if err := sub.RunMigrations(ctx); err != nil {
		log.Error().Err(err).Msg("RunMigrations failed during on-demand run")
	}

	subProvider := provider.Map[sub.Provider]
	subDataset := subProvider.Datasets()[sub.Dataset]

	// Channels for data flow
	observeChan := make(chan *data.Observation, 1000)
	saveChan := make(chan *data.Observation, 1000)
	exitChan := make(chan data.RunSummary, 1)

	var wg sync.WaitGroup

	wg.Add(1)

	go myLibrary.SaveObservations(saveChan, &wg, checks.NewInlineValidator(checks.InlineChecks()))

	// Send started event
	startedData, _ := json.Marshal(map[string]string{
		"subscription_id": sub.ID.String(),
		"name":            sub.Name,
	})
	run.events <- sseEvent{Event: "started", Data: string(startedData)}

	// Observation interceptor: summarize each record and forward to saveChan
	go func() {
		count := 0

		for obs := range observeChan {
			count++

			typ, summary := summarizeObservation(obs)

			d, _ := json.Marshal(map[string]interface{}{
				"count":   count,
				"type":    typ,
				"summary": summary,
			})
			run.events <- sseEvent{Event: "record", Data: string(d)}

			saveChan <- obs
		}

		close(saveChan)
	}()

	// Run the fetch with lookback injected into context
	fetchCtx := context.WithValue(ctx, provider.LookbackKey, lookback)
	fetchLogger := log.With().Str("SubscriptionID", sub.ID.String()).Logger()
	fetchCtx = fetchLogger.WithContext(fetchCtx)

	subDataset.Fetch(fetchCtx, sub, observeChan, exitChan)

	// Wait for fetch to complete
	summary := <-exitChan

	close(observeChan)

	// Persist run history
	if err := myLibrary.SaveRunHistory(ctx, summary); err != nil {
		log.Error().Err(err).Str("Subscription", sub.Name).Msg("failed to save run history")
	}

	// Run post-fetch hooks
	if summary.Status == data.RunSuccess && len(subDataset.PostFetch) > 0 {
		for _, hook := range subDataset.PostFetch {
			if err := hook(ctx, sub); err != nil {
				log.Error().Err(err).Str("Subscription", sub.Name).Msg("post-fetch hook failed")

				break
			}
		}
	}

	// Emit final event
	if summary.Status == data.RunFailed {
		d, _ := json.Marshal(map[string]interface{}{
			"count": summary.NumObservations,
			"error": "run failed",
		})
		run.events <- sseEvent{Event: "failed", Data: string(d)}
	} else {
		d, _ := json.Marshal(map[string]interface{}{
			"count":  summary.NumObservations,
			"status": "success",
		})
		run.events <- sseEvent{Event: "completed", Data: string(d)}
	}

	wg.Wait()
}

// RunEvents streams Server-Sent Events for an active subscription run.
func RunEvents(c *fiber.Ctx) error {
	id := c.Params("id")
	myLibrary := getLibrary(c)
	registry := getRegistry(c)

	// Resolve full subscription ID from prefix
	sub, err := myLibrary.SubscriptionFromID(c.UserContext(), id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	subID := sub.ID.String()

	run, ok := registry.Load(subID)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "no active run for this subscription",
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
					// Channel closed, run is done
					return
				}

				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data)

				if err := w.Flush(); err != nil {
					return
				}
			case <-run.done:
				// Drain any remaining events
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
