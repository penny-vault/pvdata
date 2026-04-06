package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
)

type EventType int

const (
	EventIdle EventType = iota
	EventStarted
	EventProgress
	EventCompleted
	EventFailed
)

type RunEvent struct {
	SubscriptionID   uuid.UUID
	SubscriptionName string
	Type             EventType
	RecordsCount     int
	Error            error
	Timestamp        time.Time
}

// SubscriptionStatus tracks the current state of a subscription run.
type SubscriptionStatus struct {
	Subscription *library.Subscription
	Status       EventType
	RecordsCount int
	StartTime    time.Time
	EndTime      time.Time
	Error        error
}

// RunManager orchestrates subscription execution and emits events for the TUI.
type RunManager struct {
	subscriptions []*library.Subscription
	myLibrary     *library.Library
	eventCh       chan RunEvent
	statuses      map[uuid.UUID]*SubscriptionStatus
	mu            sync.RWMutex
}

// NewRunManager creates a RunManager for the given subscriptions.
func NewRunManager(myLibrary *library.Library, subscriptions []*library.Subscription) *RunManager {
	statuses := make(map[uuid.UUID]*SubscriptionStatus, len(subscriptions))
	for _, sub := range subscriptions {
		statuses[sub.ID] = &SubscriptionStatus{
			Subscription: sub,
			Status:       EventIdle,
		}
	}

	return &RunManager{
		subscriptions: subscriptions,
		myLibrary:     myLibrary,
		eventCh:       make(chan RunEvent, 100),
		statuses:      statuses,
	}
}

// EventChan returns the channel the TUI reads events from.
func (rm *RunManager) EventChan() <-chan RunEvent {
	return rm.eventCh
}

// Statuses returns a snapshot of all subscription statuses.
func (rm *RunManager) Statuses() []*SubscriptionStatus {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	result := make([]*SubscriptionStatus, 0, len(rm.statuses))
	for _, s := range rm.statuses {
		copy := *s
		result = append(result, &copy)
	}

	return result
}

// RunAll executes all subscriptions sequentially, emitting events.
func (rm *RunManager) RunAll(ctx context.Context) {
	outChan := make(chan *data.Observation, 1000)
	exitChan := make(chan data.RunSummary, 5)

	var wg sync.WaitGroup
	wg.Add(1)

	go rm.myLibrary.SaveObservations(outChan, &wg, checks.NewInlineValidator(checks.InlineChecks()))

	// Create a counting channel that wraps outChan to track progress
	countChan := make(chan *data.Observation, 1000)
	go rm.countObservations(countChan, outChan)

	for _, subscription := range rm.subscriptions {
		rm.emit(RunEvent{
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
			Type:             EventStarted,
			Timestamp:        time.Now(),
		})

		rm.mu.Lock()
		rm.statuses[subscription.ID].Status = EventStarted
		rm.statuses[subscription.ID].StartTime = time.Now()
		rm.mu.Unlock()

		// create any needed partitions
		if err := subscription.ManagePartitions(ctx); err != nil {
			log.Error().Err(err).Msg("ManagePartitions returned an error")
		}

		// run any pending schema migrations
		if err := subscription.RunMigrations(ctx); err != nil {
			log.Error().Err(err).Msg("RunMigrations returned an error")
		}

		// resolve provider and dataset
		subProvider, ok := provider.Map[subscription.Provider]
		if !ok {
			rm.emitFailed(subscription, "provider not found: "+subscription.Provider)
			continue
		}

		subDataset, ok := subProvider.Datasets()[subscription.Dataset]
		if !ok {
			rm.emitFailed(subscription, "dataset not found: "+subscription.Dataset)
			continue
		}

		fetchLogger := log.With().Str("SubscriptionID", subscription.ID.String()).Logger()
		fetchCtx := fetchLogger.WithContext(ctx)

		subDataset.Fetch(fetchCtx, subscription, countChan, exitChan)

		// read the exit message from exitChan
		summaryMsg := <-exitChan

		// Persist run history
		if err := rm.myLibrary.SaveRunHistory(ctx, summaryMsg); err != nil {
			log.Error().Err(err).Str("subscription", subscription.Name).Msg("failed to save run history")
		}

		// Run post-fetch hooks
		if summaryMsg.Status == data.RunSuccess && len(subDataset.PostFetch) > 0 {
			for _, hook := range subDataset.PostFetch {
				if err := hook(ctx, subscription); err != nil {
					log.Error().Err(err).Str("subscription", subscription.Name).Msg("post-fetch hook failed")
					break
				}
			}
		}

		rm.mu.Lock()
		status := rm.statuses[subscription.ID]

		status.EndTime = summaryMsg.EndTime
		if summaryMsg.Status == data.RunFailed {
			status.Status = EventFailed
		} else {
			status.Status = EventCompleted
		}
		rm.mu.Unlock()

		eventType := EventCompleted
		if summaryMsg.Status == data.RunFailed {
			eventType = EventFailed
		}

		rm.emit(RunEvent{
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
			Type:             eventType,
			RecordsCount:     summaryMsg.NumObservations,
			Timestamp:        time.Now(),
		})
	}

	close(countChan)
	wg.Wait()
	close(rm.eventCh)
}

func (rm *RunManager) countObservations(in <-chan *data.Observation, out chan<- *data.Observation) {
	for obs := range in {
		rm.mu.Lock()
		if s, ok := rm.statuses[obs.SubscriptionID]; ok {
			s.RecordsCount++
			count := s.RecordsCount
			rm.mu.Unlock()

			// Emit progress every 100 records to avoid flooding
			if count%100 == 0 {
				log.Info().
					Str("Subscription", obs.SubscriptionName).
					Int("Records", count).
					Msg("import progress")
				rm.emit(RunEvent{
					SubscriptionID:   obs.SubscriptionID,
					SubscriptionName: obs.SubscriptionName,
					Type:             EventProgress,
					RecordsCount:     count,
					Timestamp:        time.Now(),
				})
			}
		} else {
			rm.mu.Unlock()
		}

		out <- obs
	}

	close(out)
}

func (rm *RunManager) emit(event RunEvent) {
	select {
	case rm.eventCh <- event:
	default:
	}
}

func (rm *RunManager) emitFailed(sub *library.Subscription, msg string) {
	rm.mu.Lock()
	rm.statuses[sub.ID].Status = EventFailed
	rm.statuses[sub.ID].Error = fmt.Errorf("%s", msg)
	rm.mu.Unlock()

	rm.emit(RunEvent{
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
		Type:             EventFailed,
		Error:            fmt.Errorf("%s", msg),
		Timestamp:        time.Now(),
	})
}
