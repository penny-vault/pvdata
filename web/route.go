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

import "github.com/gofiber/fiber/v2"

func SetupRoutes(app *fiber.App) {
	api := app.Group("/api/v1", NewAuthMiddleware())

	api.Get("/subscriptions", GetSubscriptions)
	api.Post("/subscriptions", CreateSubscription)
	api.Get("/subscriptions/:id", GetSubscription)
	api.Put("/subscriptions/:id", UpdateSubscription)
	api.Delete("/subscriptions/:id", DeleteSubscription)
	api.Post("/subscriptions/:id/activate", ActivateSubscription)
	api.Post("/subscriptions/:id/deactivate", DeactivateSubscription)

	api.Post("/subscriptions/:id/run", TriggerRun)
	api.Get("/subscriptions/:id/run/events", RunEvents)

	api.Get("/providers", GetProviders)

	api.Get("/subscriptions/:id/runs", GetRunHistory)
	api.Get("/subscriptions/:id/runs/sparkline", GetRunSparkline)

	api.Get("/subscriptions/:id/data/:datatype", GetSubscriptionData)

	api.Post("/sql", ExecuteSQL)
	api.Post("/sql/export", ExportSQL)

	api.Get("/quality/issues", GetQualityIssues)
	api.Get("/quality/summary", GetQualitySummary)
}
