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
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*
var migrationFS embed.FS

// RequiredVersion is the migration version that the application expects.
const RequiredVersion uint = 10

// Migrate runs database migrations for a data library and returns the
// resulting schema version. If migrations are already up to date, the
// returned error wraps migrate.ErrNoChange and the version reflects
// the current state of the database.
func Migrate(databaseURL string) (uint, error) {
	migrationDir, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return 0, err
	}

	migration, err := migrate.NewWithSourceInstance("iofs", migrationDir, databaseURL)
	if err != nil {
		return 0, err
	}

	upErr := migration.Up()

	version, _, vErr := migration.Version()
	if vErr != nil {
		return 0, vErr
	}

	return version, upErr
}

// CurrentVersion returns the schema version currently recorded in the
// database, or 0 if no migrations have been applied yet.
func CurrentVersion(databaseURL string) (uint, error) {
	migrationDir, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return 0, err
	}

	m, err := migrate.NewWithSourceInstance("iofs", migrationDir, databaseURL)
	if err != nil {
		return 0, err
	}

	version, _, err := m.Version()
	if err != nil {
		if err == migrate.ErrNilVersion {
			return 0, nil
		}

		return 0, err
	}

	return version, nil
}

// LatestVersion returns the highest migration version available in the
// embedded migrations directory.
func LatestVersion() (uint, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return 0, err
	}

	var latest uint

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}

		n, err := strconv.ParseUint(prefix, 10, 64)
		if err != nil {
			continue
		}

		if uint(n) > latest {
			latest = uint(n)
		}
	}

	return latest, nil
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
