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
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/penny-vault/pvdata/provider"
)

// getRegistry retrieves the RunRegistry from request context.
func getRegistry(c *fiber.Ctx) *RunRegistry {
	return c.Locals("registry").(*RunRegistry)
}

// getLogCapture retrieves the LogCapture sink from request context. Returns
// nil if log capture is not configured (e.g. tests).
func getLogCapture(c *fiber.Ctx) *LogCapture {
	v := c.Locals("logCapture")
	if v == nil {
		return nil
	}

	return v.(*LogCapture)
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

	subID := sub.ID.String()

	run, ok := registry.TryReserve(subID)
	if !ok {
		return c.Status(fiber.StatusConflict).JSON(HttpError{
			Code:    "409",
			Message: "a run is already in progress for this subscription",
		})
	}

	go RunSubscription(context.Background(), myLibrary, sub, RunOptions{
		Run:        run,
		Lookback:   lookback,
		Source:     RunSourceManual,
		LogCapture: getLogCapture(c),
	})

	return c.JSON(fiber.Map{"status": "started"})
}

// RunStatus reports whether a run is currently active for a subscription.
// Used by the UI to auto-attach SSE on page load if a run started elsewhere
// (e.g. by the scheduler).
func RunStatus(c *fiber.Ctx) error {
	id := c.Params("id")
	myLibrary := getLibrary(c)
	registry := getRegistry(c)

	sub, err := myLibrary.SubscriptionFromID(c.UserContext(), id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	return c.JSON(fiber.Map{
		"active": registry.IsActive(sub.ID.String()),
	})
}

// parseLookback parses a human-friendly duration string with suffixes:
// d (days), w (weeks), m (months), y (years). A bare number is treated as days.
func parseLookback(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty lookback value")
	}

	suffix := s[len(s)-1:]
	numStr := s[:len(s)-1]

	if suffix[0] >= '0' && suffix[0] <= '9' {
		numStr = s
		suffix = "d"
	}

	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid number in lookback %q: %w", s, err)
	}

	if n <= 0 {
		return 0, fmt.Errorf("lookback must be positive, got %d", n)
	}

	switch suffix {
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "w":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case "m":
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	case "y":
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown lookback suffix %q; use d (days), w (weeks), m (months), or y (years)", suffix)
	}
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
