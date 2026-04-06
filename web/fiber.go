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
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/penny-vault/pvdata/library"
	"github.com/spf13/viper"
)

func CreateFiberApp(myLibrary *library.Library) *fiber.App {
	app := fiber.New()
	registry := NewRunRegistry()

	// Inject shared library connection pool and run registry into request context
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("library", myLibrary)
		c.Locals("registry", registry)

		return c.Next()
	})

	// CORS configuration
	corsOrigins := viper.GetString("web.cors_origins")
	if corsOrigins == "" {
		corsOrigins = "http://localhost:5173, http://localhost:9000"
	}

	app.Use(cors.New(cors.Config{
		AllowOrigins: corsOrigins,
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
	}))

	// Or extend your config for customization
	// Logging remote IP and Port
	app.Use(logger.New(logger.Config{
		Format: "[${ip}]:${port} ${status} - ${method} ${path}\n",
	}))

	SetupRoutes(app)
	SetupSPA(app)

	return app
}
