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
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gosimple/slug"
	"github.com/penny-vault/pvdata/healthcheck"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
)

type HttpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CreateSubscriptionRequest is the JSON body for creating a new subscription.
type CreateSubscriptionRequest struct {
	Provider          string            `json:"provider"`
	Dataset           string            `json:"dataset"`
	Config            map[string]string `json:"config"`
	Schedule          string            `json:"schedule"`
	DataTypes         []string          `json:"data_types"`
	HealthCheckID     string            `json:"health_check_id"`
	CreateHealthcheck bool              `json:"create_healthcheck"`
}

// UpdateSubscriptionRequest is the JSON body for updating an existing subscription.
type UpdateSubscriptionRequest struct {
	Name          *string            `json:"name"`
	Schedule      *string            `json:"schedule"`
	Config        *map[string]string `json:"config"`
	HealthCheckID *string            `json:"health_check_id"`
	Active        *bool              `json:"active"`
}

// getLibrary retrieves the shared library connection pool from request context.
func getLibrary(c *fiber.Ctx) *library.Library {
	return c.Locals("library").(*library.Library)
}

// getScheduler retrieves the subscription scheduler from request context.
// Returns nil when the scheduler was not wired in (e.g. tests with a bare
// Fiber app).
func getScheduler(c *fiber.Ctx) *Scheduler {
	v := c.Locals("scheduler")
	if v == nil {
		return nil
	}

	return v.(*Scheduler)
}

// GetSubscriptions returns all subscriptions.
func GetSubscriptions(c *fiber.Ctx) error {
	myLibrary := getLibrary(c)

	subscriptions, err := myLibrary.Subscriptions(c.UserContext())
	if err != nil {
		log.Error().Err(err).Msg("could not load subscriptions")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load subscriptions",
		})
	}

	return c.JSON(subscriptions)
}

// GetSubscription returns a single subscription by ID prefix.
func GetSubscription(c *fiber.Ctx) error {
	id := c.Params("id")
	myLibrary := getLibrary(c)

	sub, err := myLibrary.SubscriptionFromID(c.UserContext(), id)
	if err != nil {
		log.Error().Err(err).Str("ID", id).Msg("subscription not found")

		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "subscription not found",
		})
	}

	return c.JSON(sub)
}

// CreateSubscription creates a new subscription from JSON body.
func CreateSubscription(c *fiber.Ctx) error {
	var req CreateSubscriptionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid request body",
		})
	}

	if req.Provider == "" || req.Dataset == "" {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "provider and dataset are required",
		})
	}

	myLibrary := getLibrary(c)

	sub, err := provider.NewSubscription(req.Provider, req.Dataset, req.Config, req.DataTypes, myLibrary)
	if err != nil {
		log.Error().Err(err).Msg("could not create subscription")

		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: err.Error(),
		})
	}

	if req.Schedule != "" {
		sub.Schedule = req.Schedule
	}

	switch {
	case req.CreateHealthcheck:
		idShort := sub.ID.String()[:5]
		checkSlug := slug.Make(fmt.Sprintf("%s %s %s %s", sub.Name, sub.Provider, sub.Dataset, idShort))

		checkID, err := healthcheck.Create(
			fmt.Sprintf("%s %s (%s)", sub.Name, sub.Dataset, idShort),
			checkSlug,
			sub.DataTypes,
			sub.Schedule,
		)
		if err != nil {
			log.Error().Err(err).Msg("could not create healthchecks.io monitor")

			return c.Status(fiber.StatusBadGateway).JSON(HttpError{
				Code:    "502",
				Message: "could not create healthchecks.io monitor: " + err.Error(),
			})
		}

		sub.HealthCheckID = checkID
	case req.HealthCheckID != "":
		sub.HealthCheckID = req.HealthCheckID
	}

	if err := sub.Save(c.UserContext()); err != nil {
		log.Error().Err(err).Msg("could not save subscription")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not save subscription",
		})
	}

	if sub.Active {
		if scheduler := getScheduler(c); scheduler != nil {
			if err := scheduler.Schedule(sub); err != nil {
				log.Warn().Err(err).
					Str("subscription_id", sub.ID.String()).
					Str("schedule", sub.Schedule).
					Msg("could not register cron job for new subscription")
			}
		}
	}

	return c.Status(fiber.StatusCreated).JSON(sub)
}

// UpdateSubscription updates fields on an existing subscription.
func UpdateSubscription(c *fiber.Ctx) error {
	id := c.Params("id")

	var req UpdateSubscriptionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid request body",
		})
	}

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

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}
	defer conn.Release()

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return c.Status(fiber.StatusBadRequest).JSON(HttpError{
				Code:    "400",
				Message: "name cannot be empty",
			})
		}

		if _, err := conn.Exec(ctx, "UPDATE subscriptions SET name=$1 WHERE id=$2", name, sub.ID); err != nil {
			log.Error().Err(err).Msg("could not update name")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not update subscription",
			})
		}

		sub.Name = name
	}

	if req.Schedule != nil {
		if _, err := conn.Exec(ctx, "UPDATE subscriptions SET schedule=$1 WHERE id=$2", *req.Schedule, sub.ID); err != nil {
			log.Error().Err(err).Msg("could not update schedule")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not update subscription",
			})
		}

		sub.Schedule = *req.Schedule

		if sub.HealthCheckID != "" {
			if err := healthcheck.UpdateSchedule(sub.HealthCheckID, sub.Schedule); err != nil {
				log.Warn().Err(err).
					Str("subscription_id", sub.ID.String()).
					Str("health_check_id", sub.HealthCheckID).
					Str("schedule", sub.Schedule).
					Msg("could not sync schedule to healthchecks.io")
			}
		}

		if scheduler := getScheduler(c); scheduler != nil && sub.Active {
			if err := scheduler.Schedule(sub); err != nil {
				log.Warn().Err(err).
					Str("subscription_id", sub.ID.String()).
					Str("schedule", sub.Schedule).
					Msg("could not reschedule cron job after edit")
			}
		}
	}

	if req.Config != nil {
		if _, err := conn.Exec(ctx, "UPDATE subscriptions SET config=$1 WHERE id=$2", *req.Config, sub.ID); err != nil {
			log.Error().Err(err).Msg("could not update config")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not update subscription",
			})
		}

		sub.Config = *req.Config
	}

	if req.HealthCheckID != nil {
		if _, err := conn.Exec(ctx, "UPDATE subscriptions SET health_check_id=$1 WHERE id=$2", *req.HealthCheckID, sub.ID); err != nil {
			log.Error().Err(err).Msg("could not update health_check_id")

			return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
				Code:    "500",
				Message: "could not update subscription",
			})
		}

		sub.HealthCheckID = *req.HealthCheckID
	}

	if req.Active != nil {
		if *req.Active {
			if err := sub.Activate(ctx); err != nil {
				log.Error().Err(err).Msg("could not activate subscription")

				return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
					Code:    "500",
					Message: "could not activate subscription",
				})
			}
		} else {
			if err := sub.Deactivate(ctx); err != nil {
				log.Error().Err(err).Msg("could not deactivate subscription")

				return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
					Code:    "500",
					Message: "could not deactivate subscription",
				})
			}
		}

		sub.Active = *req.Active

		if scheduler := getScheduler(c); scheduler != nil {
			if sub.Active {
				if err := scheduler.Schedule(sub); err != nil {
					log.Warn().Err(err).Str("subscription_id", sub.ID.String()).Msg("could not register cron job on activate")
				}
			} else {
				if err := scheduler.Unschedule(sub.ID); err != nil {
					log.Warn().Err(err).Str("subscription_id", sub.ID.String()).Msg("could not remove cron job on deactivate")
				}
			}
		}
	}

	return c.JSON(sub)
}

// DeleteSubscription deletes a subscription by ID.
func DeleteSubscription(c *fiber.Ctx) error {
	id := c.Params("id")
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

	if err := sub.Delete(ctx); err != nil {
		log.Error().Err(err).Msg("could not delete subscription")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not delete subscription",
		})
	}

	if scheduler := getScheduler(c); scheduler != nil {
		if err := scheduler.Unschedule(sub.ID); err != nil {
			log.Warn().Err(err).Str("subscription_id", sub.ID.String()).Msg("could not remove cron job on delete")
		}
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ActivateSubscription activates a subscription by ID.
func ActivateSubscription(c *fiber.Ctx) error {
	id := c.Params("id")
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

	if err := sub.Activate(ctx); err != nil {
		log.Error().Err(err).Msg("could not activate subscription")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not activate subscription",
		})
	}

	sub.Active = true

	if scheduler := getScheduler(c); scheduler != nil {
		if err := scheduler.Schedule(sub); err != nil {
			log.Warn().Err(err).Str("subscription_id", sub.ID.String()).Msg("could not register cron job on activate")
		}
	}

	return c.JSON(fiber.Map{"status": "activated"})
}

// DeactivateSubscription deactivates a subscription by ID.
func DeactivateSubscription(c *fiber.Ctx) error {
	id := c.Params("id")
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

	if err := sub.Deactivate(ctx); err != nil {
		log.Error().Err(err).Msg("could not deactivate subscription")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not deactivate subscription",
		})
	}

	if scheduler := getScheduler(c); scheduler != nil {
		if err := scheduler.Unschedule(sub.ID); err != nil {
			log.Warn().Err(err).Str("subscription_id", sub.ID.String()).Msg("could not remove cron job on deactivate")
		}
	}

	return c.JSON(fiber.Map{"status": "deactivated"})
}
