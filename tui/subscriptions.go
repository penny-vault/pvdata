package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

type SubscriptionsModel struct {
	table    table.Model
	statuses map[uuid.UUID]*SubscriptionStatus
	width    int
	height   int
}

func NewSubscriptionsModel(statuses []*SubscriptionStatus) SubscriptionsModel {
	statusMap := make(map[uuid.UUID]*SubscriptionStatus, len(statuses))
	for _, s := range statuses {
		statusMap[s.Subscription.ID] = s
	}

	columns := []table.Column{
		{Title: "Name", Width: 25},
		{Title: "Provider/Dataset", Width: 25},
		{Title: "Status", Width: 10},
		{Title: "Records", Width: 10},
		{Title: "Last Run", Width: 20},
	}

	rows := buildRows(statuses)

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(10),
	)

	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)
	t.SetStyles(s)

	return SubscriptionsModel{
		table:    t,
		statuses: statusMap,
	}
}

func (m SubscriptionsModel) Init() tea.Cmd {
	return nil
}

func (m SubscriptionsModel) Update(msg tea.Msg) (SubscriptionsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height - 4
		m.table.SetHeight(m.height)

	case RunEvent:
		if s, ok := m.statuses[msg.SubscriptionID]; ok {
			s.Status = msg.Type
			if msg.RecordsCount > 0 {
				s.RecordsCount = msg.RecordsCount
			}
			if msg.Error != nil {
				s.Error = msg.Error
			}
			if msg.Type == EventCompleted || msg.Type == EventFailed {
				s.EndTime = msg.Timestamp
			}
		}
		m.refreshRows()
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m SubscriptionsModel) View() string {
	return m.table.View()
}

func (m *SubscriptionsModel) refreshRows() {
	statuses := make([]*SubscriptionStatus, 0, len(m.statuses))
	for _, s := range m.statuses {
		statuses = append(statuses, s)
	}
	m.table.SetRows(buildRows(statuses))
}

func buildRows(statuses []*SubscriptionStatus) []table.Row {
	rows := make([]table.Row, 0, len(statuses))
	for _, s := range statuses {
		statusStr := "idle"
		switch s.Status {
		case EventStarted:
			statusStr = "running"
		case EventCompleted:
			statusStr = "done"
		case EventFailed:
			statusStr = "error"
		case EventProgress:
			statusStr = "running"
		}

		lastRun := "-"
		if !s.Subscription.LastRun.IsZero() {
			lastRun = s.Subscription.LastRun.Format(time.DateTime)
		}

		rows = append(rows, table.Row{
			s.Subscription.Name,
			fmt.Sprintf("%s/%s", s.Subscription.Provider, s.Subscription.Dataset),
			statusStr,
			fmt.Sprintf("%d", s.RecordsCount),
			lastRun,
		})
	}
	return rows
}
