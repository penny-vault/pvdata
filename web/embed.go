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
	"embed"
	"io/fs"
	"net/http"

	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/rs/zerolog/log"

	"github.com/gofiber/fiber/v2"
)

//go:embed ui/dist/*
var uiAssets embed.FS

// SetupSPA configures the Fiber app to serve the embedded Vue SPA.
// Static files are served from the embedded ui/dist directory.
// Non-matching routes fall back to index.html for SPA history-mode routing
// via the filesystem middleware's NotFoundFile option.
func SetupSPA(app *fiber.App) {
	distFS, err := fs.Sub(uiAssets, "ui/dist")
	if err != nil {
		log.Warn().Err(err).Msg("could not load embedded UI assets, SPA serving disabled")
		return
	}

	app.Use("/", filesystem.New(filesystem.Config{
		Root:         http.FS(distFS),
		Browse:       false,
		Index:        "index.html",
		NotFoundFile: "index.html",
	}))
}
