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
	"errors"
	"sync"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/library"
)

// SubscriptionRunner is invoked by Scheduler when a subscription's cron
// fires. It receives only the subscription id so the runner can load the
// current subscription state from the library, ensuring edits made after
// scheduling (config, schedule, etc.) are reflected on the next run.
type SubscriptionRunner func(subID uuid.UUID)

// Scheduler wraps a gocron.Scheduler with per-subscription job tracking so
// the web handlers can add, remove, and replace jobs as subscriptions are
// created, edited, activated, or deactivated.
type Scheduler struct {
	inner  gocron.Scheduler
	runner SubscriptionRunner

	mu   sync.Mutex
	jobs map[uuid.UUID]uuid.UUID
}

// NewScheduler wraps the given gocron.Scheduler. The runner is called on
// every cron fire with the subscription id.
func NewScheduler(inner gocron.Scheduler, runner SubscriptionRunner) *Scheduler {
	return &Scheduler{
		inner:  inner,
		runner: runner,
		jobs:   make(map[uuid.UUID]uuid.UUID),
	}
}

// Schedule registers (or replaces) the cron job for sub. No-op when sub or
// sub.Schedule is empty.
func (s *Scheduler) Schedule(sub *library.Subscription) error {
	if sub == nil || sub.Schedule == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.jobs[sub.ID]; ok {
		if err := s.inner.RemoveJob(existing); err != nil && !errors.Is(err, gocron.ErrJobNotFound) {
			return err
		}

		delete(s.jobs, sub.ID)
	}

	subID := sub.ID

	job, err := s.inner.NewJob(
		gocron.CronJob(sub.Schedule, false),
		gocron.NewTask(func() {
			s.runner(subID)
		}),
	)
	if err != nil {
		return err
	}

	s.jobs[sub.ID] = job.ID()

	return nil
}

// Unschedule removes the cron job registered for the subscription, if any.
func (s *Scheduler) Unschedule(subID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.jobs[subID]
	if !ok {
		return nil
	}

	if err := s.inner.RemoveJob(existing); err != nil && !errors.Is(err, gocron.ErrJobNotFound) {
		return err
	}

	delete(s.jobs, subID)

	return nil
}
