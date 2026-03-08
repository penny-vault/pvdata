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
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

type HttpError struct {
	Code    string `json:"code"`
	Message string `json:"messsage"`
}

func GetSubscriptions(c *fiber.Ctx) error {
	ctx := c.UserContext()

	myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
	if err != nil {
		log.Fatal().Err(err).Msg("could not load library info")
		return c.JSON(HttpError{
			Code:    "501",
			Message: "could not load library info",
		})
	}

	subscriptions, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("could not load subscriptions")
		return c.JSON(HttpError{
			Code:    "501",
			Message: "could not load library info",
		})
	}

	return c.JSON(subscriptions)
}
