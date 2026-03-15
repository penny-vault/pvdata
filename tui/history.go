package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
)

// SessionRun tracks a completed run in the current session.
type SessionRun struct {
	SubscriptionID   uuid.UUID
	SubscriptionName string
	Status           EventType
	RecordsCount     int
	StartTime        time.Time
	EndTime          time.Time
}

type HistoryModel struct {
	viewport    viewport.Model
	sessionRuns []SessionRun
	startTimes  map[uuid.UUID]time.Time
	ready       bool
}

func NewHistoryModel() HistoryModel {
	return HistoryModel{
		sessionRuns: make([]SessionRun, 0),
		startTimes:  make(map[uuid.UUID]time.Time),
	}
}

func (m HistoryModel) Init() tea.Cmd {
	return nil
}

func (m HistoryModel) Update(msg tea.Msg) (HistoryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport = viewport.New(viewport.WithWidth(msg.Width), viewport.WithHeight(msg.Height-6))
		m.ready = true
		m.refreshContent()

	case RunEvent:
		if msg.Type == EventStarted {
			m.startTimes[msg.SubscriptionID] = msg.Timestamp
		}

		if msg.Type == EventCompleted || msg.Type == EventFailed {
			startTime := m.startTimes[msg.SubscriptionID]
			m.sessionRuns = append(m.sessionRuns, SessionRun{
				SubscriptionID:   msg.SubscriptionID,
				SubscriptionName: msg.SubscriptionName,
				Status:           msg.Type,
				RecordsCount:     msg.RecordsCount,
				StartTime:        startTime,
				EndTime:          msg.Timestamp,
			})
			m.refreshContent()
		}
	}

	if m.ready {
		var cmd tea.Cmd

		m.viewport, cmd = m.viewport.Update(msg)

		return m, cmd
	}

	return m, nil
}

func (m HistoryModel) View() string {
	if !m.ready {
		return "Initializing history..."
	}

	return m.viewport.View()
}

func (m *HistoryModel) refreshContent() {
	if !m.ready {
		return
	}

	var b strings.Builder

	b.WriteString("-- Current Session --\n\n")

	if len(m.sessionRuns) == 0 {
		b.WriteString("  No completed runs yet.\n")
	} else {
		for _, run := range m.sessionRuns {
			status := "done"
			if run.Status == EventFailed {
				status = "FAILED"
			}

			duration := run.EndTime.Sub(run.StartTime).Round(time.Second)
			fmt.Fprintf(&b, "  %-30s  %-8s  %d records  %s\n",
				run.SubscriptionName, status, run.RecordsCount, duration)
		}
	}

	m.viewport.SetContent(b.String())
}
