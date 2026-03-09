package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TickMsg is sent every second to update the status bar timer.
type TickMsg time.Time

type StatusBarModel struct {
	totalSubscriptions int
	runningCount       int
	completedCount     int
	failedCount        int
	totalRecords       int
	startTime          time.Time
	width              int
}

func NewStatusBarModel(totalSubscriptions int) StatusBarModel {
	return StatusBarModel{
		totalSubscriptions: totalSubscriptions,
		startTime:          time.Now(),
	}
}

func (m StatusBarModel) Init() tea.Cmd {
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

func (m StatusBarModel) Update(msg tea.Msg) (StatusBarModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width

	case TickMsg:
		return m, tickCmd()

	case RunEvent:
		switch msg.Type {
		case EventStarted:
			m.runningCount++
		case EventProgress:
			m.totalRecords = msg.RecordsCount
		case EventCompleted:
			m.runningCount--
			m.completedCount++
			m.totalRecords = msg.RecordsCount
		case EventFailed:
			m.runningCount--
			m.failedCount++
		}
	}

	return m, nil
}

func (m StatusBarModel) View() string {
	elapsed := time.Since(m.startTime).Round(time.Second)

	status := fmt.Sprintf(" running %d/%d", m.runningCount, m.totalSubscriptions)
	if m.completedCount > 0 {
		status += fmt.Sprintf(" | done %d", m.completedCount)
	}

	if m.failedCount > 0 {
		status += fmt.Sprintf(" | failed %d", m.failedCount)
	}

	status += fmt.Sprintf(" | %d records | %s", m.totalRecords, elapsed)

	return StatusBarStyle.Width(m.width).Render(status)
}
