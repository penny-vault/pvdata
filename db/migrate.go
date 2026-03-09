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
package db

import (
	"embed"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*
var migrationFS embed.FS

// Migrate runs database migrations for a data library
func Migrate(databaseURL string) error {
	migrationDir, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return err
	}

	migration, err := migrate.NewWithSourceInstance("iofs", migrationDir, databaseURL)
	if err != nil {
		return err
	}

	err = migration.Up()

	return err
}
