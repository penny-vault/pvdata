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
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*
var migrationFS embed.FS

// RequiredVersion is the migration version that the application expects.
const RequiredVersion uint = 7

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

// CheckVersion verifies the database is at the required migration version.
// Returns an error if the version is wrong or the database is dirty.
func CheckVersion(databaseURL string) error {
	migrationDir, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", migrationDir, databaseURL)
	if err != nil {
		return err
	}

	version, dirty, err := m.Version()
	if err != nil {
		return fmt.Errorf("could not read database migration version: %w", err)
	}

	if dirty {
		return fmt.Errorf("database is in a dirty state at version %d; fix manually and retry", version)
	}

	if version < RequiredVersion {
		return fmt.Errorf("database is at version %d but version %d is required; run 'pvdata init --from-config' to apply migrations", version, RequiredVersion)
	}

	return nil
}
