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
package checks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditOptions controls which checks run and over what time range.
type AuditOptions struct {
	Lookback  *time.Duration
	Full      bool
	DataTypes []string
	Checks    []string
}

// AuditRunner executes a set of AuditChecks and manages run checkpoints.
type AuditRunner struct {
	checks []AuditCheck
	pool   *pgxpool.Pool
}

// NewAuditRunner creates an AuditRunner with the given checks and pool.
func NewAuditRunner(checks []AuditCheck, pool *pgxpool.Pool) *AuditRunner {
	return &AuditRunner{checks: checks, pool: pool}
}

// Run executes all matching checks against table and returns the combined results.
func (r *AuditRunner) Run(ctx context.Context, opts AuditOptions, table string) ([]CheckResult, error) {
	var allResults []CheckResult

	for _, c := range r.checks {
		if len(opts.DataTypes) > 0 && !matchesDataType(c, opts.DataTypes) {
			continue
		}

		if len(opts.Checks) > 0 && !matchesCheckName(c, opts.Checks) {
			continue
		}

		var lastChecked *time.Time
		if !opts.Full {
			lastChecked = r.loadCheckpoint(ctx, c.Name())
		}

		results, err := c.Audit(ctx, r.pool, table, lastChecked, opts.Lookback)
		if err != nil {
			return allResults, err
		}

		allResults = append(allResults, results...)

		if r.pool != nil {
			r.saveCheckpoint(ctx, c.Name())
		}
	}

	return allResults, nil
}

func (r *AuditRunner) loadCheckpoint(ctx context.Context, checkName string) *time.Time {
	if r.pool == nil {
		return nil
	}

	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil
	}

	defer conn.Release()

	var lastRun time.Time

	err = conn.QueryRow(ctx,
		"SELECT last_run FROM audit_checkpoints WHERE check_name = $1", checkName).Scan(&lastRun)
	if err != nil {
		return nil
	}

	return &lastRun
}

func (r *AuditRunner) saveCheckpoint(ctx context.Context, checkName string) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return
	}

	defer conn.Release()

	_, _ = conn.Exec(ctx,
		`INSERT INTO audit_checkpoints (check_name, last_run)
		 VALUES ($1, now())
		 ON CONFLICT (check_name) DO UPDATE SET last_run = now()`, checkName)
}

func matchesDataType(c Check, dataTypes []string) bool {
	for _, dt := range c.DataTypes() {
		for _, want := range dataTypes {
			if dt == want {
				return true
			}
		}
	}

	return false
}

func matchesCheckName(c Check, names []string) bool {
	for _, name := range names {
		if c.Name() == name {
			return true
		}
	}

	return false
}
