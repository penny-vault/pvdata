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
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/healthcheck"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// RunSource labels the trigger origin of a subscription run for logs and
// healthcheck ping bodies.
type RunSource string

const (
	RunSourceScheduled RunSource = "scheduled"
	RunSourceManual    RunSource = "manual"
)

// runProgressInterval bounds how often we UPDATE num_observations
// on a running run_history row. FRED-style providers can emit
// hundreds of thousands of records per minute; this caps the
// write rate regardless of throughput.
const runProgressInterval = 10 * time.Second

// runLogFlushInterval bounds how often we copy the captured log
// buffer into run_history.log while the run is in progress. The
// final drain at run-end always writes one more time, so this
// interval only affects how much of the log survives an abrupt
// process exit (panic, OOM, restart). Five seconds keeps the
// write rate modest while preserving useful diagnostics.
const runLogFlushInterval = 5 * time.Second

// RunOptions configures a subscription run.
type RunOptions struct {
	// Run is the registry-managed activeRun. Required when callers want SSE
	// streaming or auto-attach support; pre-reserve via registry.TryReserve.
	Run *activeRun
	// Lookback overrides the default fetch window when non-zero.
	Lookback time.Duration
	// Source labels the run for logs / healthcheck bodies.
	Source RunSource
	// LogCapture, when set, drains the per-run log buffer so it can be
	// persisted alongside run history. Set by the serve command at startup.
	LogCapture *LogCapture
}

// RunSubscription executes a fetch for the subscription, persists observations,
// saves run history, runs post-fetch hooks, publishes SSE events (if a run is
// attached), and pings healthchecks at start and finish.
//
// The caller must reserve a registry slot via TryReserve and pass the resulting
// activeRun in opts.Run. RunSubscription owns the run lifecycle thereafter and
// will call run.finish() before returning.
func RunSubscription(ctx context.Context, lib *library.Library, sub *library.Subscription, opts RunOptions) {
	if opts.Run != nil {
		defer opts.Run.finish()
	}

	// Derive a cancellable context so the cancel handler can stop the fetch.
	// Stored on the activeRun so external HTTP cancel calls reach this run.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if opts.Run != nil {
		opts.Run.cancel = cancel
	}

	source := opts.Source
	if source == "" {
		source = RunSourceManual
	}

	logger := log.With().
		Str("subscription", sub.Name).
		Str("source", string(source)).
		Logger()
	logger.Info().Msg("starting subscription run")

	pingHealthcheck(sub, healthcheck.PingStart, fmt.Sprintf("starting %s run of %s", source, sub.Name))

	if err := sub.ManagePartitions(ctx); err != nil {
		logger.Error().Err(err).Msg("ManagePartitions failed")
	}

	if err := sub.RunMigrations(ctx); err != nil {
		logger.Error().Err(err).Msg("RunMigrations failed")
	}

	subProvider, ok := provider.Map[sub.Provider]
	if !ok {
		logger.Error().Str("provider", sub.Provider).Msg("provider not found")
		emitFinal(opts.Run, sub, 0, false, "provider not found")
		pingHealthcheck(sub, healthcheck.PingFail, fmt.Sprintf("provider not found: %s", sub.Provider))

		return
	}

	subDataset, ok := subProvider.Datasets()[sub.Dataset]
	if !ok {
		logger.Error().Str("dataset", sub.Dataset).Msg("dataset not found")
		emitFinal(opts.Run, sub, 0, false, "dataset not found")
		pingHealthcheck(sub, healthcheck.PingFail, fmt.Sprintf("dataset not found: %s", sub.Dataset))

		return
	}

	runID, beginErr := lib.BeginRun(ctx, data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
	})
	if beginErr != nil {
		logger.Error().Err(beginErr).Msg("could not insert running run_history row")
	}

	if opts.Run != nil && runID != "" {
		ridCopy := runID
		opts.Run.runID.Store(&ridCopy)
	}

	// Throttle progress updates so high-throughput providers (FRED-style)
	// don't drown the DB in writes; one UPDATE per runProgressInterval.
	progress := NewProgressThrottle(runProgressInterval, func(n int) {
		if err := lib.UpdateRunProgress(ctx, runID, n); err != nil {
			logger.Warn().Err(err).Msg("could not update run progress")
		}
	})

	// Periodically flush the captured log buffer into run_history.log so
	// the persisted log survives an abrupt process exit (panic, OOM,
	// restart). The final Drain at run-end writes one more time after
	// stopFlusher is closed, so flushes do not race with the final write.
	stopFlusher := make(chan struct{})

	var flusherDone sync.WaitGroup

	if opts.LogCapture != nil && runID != "" {
		flusherDone.Add(1)

		go func() {
			defer flusherDone.Done()

			ticker := time.NewTicker(runLogFlushInterval)
			defer ticker.Stop()

			for {
				select {
				case <-stopFlusher:
					return
				case <-ticker.C:
					snapshot := opts.LogCapture.Snapshot(sub.ID.String())
					if snapshot == "" {
						continue
					}

					if err := lib.UpdateRunLog(ctx, runID, snapshot); err != nil {
						logger.Warn().Err(err).Msg("could not flush run log")
					}
				}
			}
		}()
	}

	observeChan := make(chan *data.Observation, 1000)
	saveChan := make(chan *data.Observation, 1000)
	exitChan := make(chan data.RunSummary, 1)

	var wg sync.WaitGroup

	wg.Add(1)

	go lib.SaveObservations(saveChan, &wg, nil)

	emitStarted(opts.Run, sub)

	// Observation interceptor: summarize each record, push throttled
	// progress to the DB, and forward to saveChan.
	go func() {
		count := 0

		for obs := range observeChan {
			count++

			typ, summary := summarizeObservation(obs)

			if opts.Run != nil {
				d, _ := json.Marshal(map[string]interface{}{
					"count":   count,
					"type":    typ,
					"summary": summary,
				})
				opts.Run.publish(sseEvent{Event: "record", Data: string(d)})
			}

			progress.Tick(time.Now(), count)

			saveChan <- obs
		}

		close(saveChan)
	}()

	fetchLogger := logger.With().Str("SubscriptionID", sub.ID.String()).Logger()
	fetchCtx := fetchLogger.WithContext(ctx)

	if opts.Lookback > 0 {
		fetchCtx = context.WithValue(fetchCtx, provider.LookbackKey, opts.Lookback)
	}

	subDataset.Fetch(fetchCtx, sub, observeChan, exitChan)

	summary := <-exitChan

	close(observeChan)
	wg.Wait()

	progress.Flush()

	// If the run was cancelled (soft or force), record cancelled status
	// rather than whatever the provider reported on its way out.
	cancelled := opts.Run != nil && opts.Run.cancelled.Load()
	if cancelled {
		summary.Status = data.RunCancelled
	}

	// FinalizeRun is conditional on status='running' so a force-cancel that
	// already wrote 'cancelled' is preserved. Use a background context so
	// the write still happens even if the run's context was cancelled.
	if err := lib.FinalizeRun(context.Background(), runID, summary); err != nil {
		logger.Error().Err(err).Msg("failed to finalise run history")
	}

	logQualitySummary(ctx, lib, sub, summary)

	if summary.Status == data.RunSuccess && len(subDataset.PostFetch) > 0 {
		for _, hook := range subDataset.PostFetch {
			if err := hook(ctx, sub); err != nil {
				logger.Error().Err(err).Msg("post-fetch hook failed")

				break
			}
		}
	}

	duration := summary.EndTime.Sub(summary.StartTime).Round(time.Second)

	// Healthcheck ping first — any warning it logs should land in both the
	// live stream and the persisted log.
	switch summary.Status {
	case data.RunCancelled:
		pingHealthcheck(sub, healthcheck.PingFail,
			fmt.Sprintf("%s run of %s cancelled after %s (%d observations)",
				source, sub.Name, duration, summary.NumObservations))
	case data.RunFailed:
		pingHealthcheck(sub, healthcheck.PingFail,
			fmt.Sprintf("%s run of %s failed after %s (%d observations)",
				source, sub.Name, duration, summary.NumObservations))
	default:
		pingHealthcheck(sub, healthcheck.PingSuccess,
			fmt.Sprintf("%s run of %s succeeded in %s (%d observations)",
				source, sub.Name, duration, summary.NumObservations))
	}

	// After a successful run, retune the healthcheck grace period from
	// observed history so monitoring tracks the workload over time.
	if summary.Status == data.RunSuccess {
		updateHealthcheckGrace(ctx, lib, sub, subDataset.ExpectedDuration, logger)
	}

	// Final summary line is the last log we emit for the run.
	switch summary.Status {
	case data.RunCancelled:
		logger.Warn().Int("observations", summary.NumObservations).Msg("subscription run cancelled")
	case data.RunFailed:
		logger.Error().Int("observations", summary.NumObservations).Msg("subscription run failed")
	default:
		logger.Info().Int("observations", summary.NumObservations).Msg("subscription run completed")
	}

	// Emit the final SSE event LAST — after this, attached UIs close the
	// connection, so any later log lines wouldn't reach them.
	switch summary.Status {
	case data.RunCancelled:
		emitCancelled(opts.Run, summary.NumObservations)
	case data.RunFailed:
		emitFinal(opts.Run, sub, summary.NumObservations, false, "run failed")
	default:
		emitFinal(opts.Run, sub, summary.NumObservations, true, "")
	}

	// Drain the captured log buffer LAST so post-fetch hooks, healthcheck
	// pings, and the final "subscription run completed" line are all
	// included — matching what live SSE clients saw. Stop the periodic
	// flusher first so its UPDATE cannot race with this final write.
	// Use a background context so the final log is persisted even when
	// the run was cancelled (which cancelled ctx).
	if opts.LogCapture != nil && runID != "" {
		close(stopFlusher)
		flusherDone.Wait()

		runLog := opts.LogCapture.Drain(sub.ID.String())
		if runLog != "" {
			if err := lib.UpdateRunLog(context.Background(), runID, runLog); err != nil {
				logger.Error().Err(err).Msg("failed to save run log")
			}
		}
	}
}

func emitStarted(run *activeRun, sub *library.Subscription) {
	if run == nil {
		return
	}

	d, _ := json.Marshal(map[string]string{
		"subscription_id": sub.ID.String(),
		"name":            sub.Name,
	})
	run.publish(sseEvent{Event: "started", Data: string(d)})
}

func emitCancelled(run *activeRun, count int) {
	if run == nil {
		return
	}

	d, _ := json.Marshal(map[string]interface{}{
		"count":  count,
		"status": "cancelled",
	})
	run.publish(sseEvent{Event: "cancelled", Data: string(d)})
}

func emitFinal(run *activeRun, _ *library.Subscription, count int, success bool, errMsg string) {
	if run == nil {
		return
	}

	if success {
		d, _ := json.Marshal(map[string]interface{}{
			"count":  count,
			"status": "success",
		})
		run.publish(sseEvent{Event: "completed", Data: string(d)})

		return
	}

	d, _ := json.Marshal(map[string]interface{}{
		"count": count,
		"error": errMsg,
	})
	run.publish(sseEvent{Event: "failed", Data: string(d)})
}

// graceHistoryWindow bounds how many recent successful runs feed the
// rolling average used to retune the healthcheck grace. Small enough
// to adapt to genuine workload changes; large enough to smooth one-off
// fast or slow runs.
const graceHistoryWindow = 10

// updateHealthcheckGrace recomputes the healthcheck grace period from the
// average of the last graceHistoryWindow successful runs (or falls back to
// the dataset's declared expected duration when history is too thin) and
// pushes the result to healthchecks.io. Errors are logged as warnings; a
// failed grace update never aborts the surrounding run.
func updateHealthcheckGrace(ctx context.Context, lib *library.Library, sub *library.Subscription, expected time.Duration, logger zerolog.Logger) {
	if sub.HealthCheckID == "" {
		return
	}

	avg, ok, err := lib.AvgSuccessfulRunDuration(ctx, sub.ID.String(), graceHistoryWindow)
	if err != nil {
		logger.Warn().Err(err).Msg("could not compute average run duration for healthcheck grace update")
		return
	}

	baseline := avg
	if !ok {
		baseline = expected
	}

	grace := healthcheck.GraceFromExpected(baseline)

	if err := healthcheck.UpdateGrace(sub.HealthCheckID, grace); err != nil {
		logger.Warn().Err(err).Int("grace_seconds", grace).Msg("healthcheck grace update failed")
		return
	}

	logger.Info().
		Int("grace_seconds", grace).
		Dur("baseline", baseline).
		Bool("from_history", ok).
		Msg("healthcheck grace retuned")
}

func pingHealthcheck(sub *library.Subscription, kind healthcheck.PingKind, body string) {
	if sub.HealthCheckID == "" {
		return
	}

	if err := healthcheck.Ping(sub.HealthCheckID, kind, body); err != nil {
		log.Warn().Err(err).
			Str("subscription", sub.Name).
			Str("kind", string(kind)).
			Msg("healthcheck ping failed")
	}
}

func logQualitySummary(ctx context.Context, lib *library.Library, sub *library.Subscription, summary data.RunSummary) {
	qualityConn, qErr := lib.AcquireWithTimeout(ctx)
	if qErr != nil {
		return
	}
	defer qualityConn.Release()

	var critCount, errCount, warnCount int

	_ = qualityConn.QueryRow(ctx,
		`SELECT
			coalesce(sum(case when severity='critical' then 1 else 0 end), 0),
			coalesce(sum(case when severity='error' then 1 else 0 end), 0),
			coalesce(sum(case when severity='warning' then 1 else 0 end), 0)
		FROM data_quality_issues
		WHERE subscription_id = $1 AND detected_at > $2`,
		sub.ID, summary.StartTime).Scan(&critCount, &errCount, &warnCount)

	if critCount+errCount+warnCount > 0 {
		log.Warn().
			Str("subscription", sub.Name).
			Int("critical", critCount).
			Int("errors", errCount).
			Int("warnings", warnCount).
			Msg("data quality issues detected (run `pvdata check` for details)")
	}
}
