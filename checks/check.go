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
	"github.com/penny-vault/pvdata/data"
)

type CheckSeverity int

const (
	SeverityInfo CheckSeverity = iota
	SeverityWarning
	SeverityError
	SeverityCritical
)

func (s CheckSeverity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	case SeverityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

type CheckPhase int

const (
	PhaseInline CheckPhase = iota
	PhaseAudit
	PhaseBoth
)

type CheckResult struct {
	CheckName     string
	Severity      CheckSeverity
	Ticker        string
	CompositeFigi string
	Dimension     string
	EventDate     time.Time
	Field         string
	Message       string
	Expected      string
	Actual        string
	DataType      string
}

type Check interface {
	Name() string
	Description() string
	Phase() CheckPhase
	Severity() CheckSeverity
	DataTypes() []string
}

type InlineCheck interface {
	Check
	Validate(ctx context.Context, obs *data.Observation) ([]CheckResult, bool)
}

type AuditCheck interface {
	Check
	Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, lookback *time.Duration) ([]CheckResult, error)
}
