package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/penny-vault/pvdata/library"
)

type tabID int

const (
	tabSubscriptions tabID = iota
	tabLogs
	tabHistory
	tabConfig
)

var tabNames = []string{"Subscriptions", "Logs", "History", "Config"}

type MainModel struct {
	activeTab     tabID
	subscriptions SubscriptionsModel
	logs          LogsModel
	history       HistoryModel
	config        ConfigModel
	statusBar     StatusBarModel
	runManager    *RunManager
	width         int
	height        int
	quitting      bool
}

func NewMainModel(
	myLibrary *library.Library,
	runManager *RunManager,
	logCh <-chan LogEntry,
) MainModel {
	statuses := runManager.Statuses()

	return MainModel{
		activeTab:     tabSubscriptions,
		subscriptions: NewSubscriptionsModel(statuses),
		logs:          NewLogsModel(logCh),
		history:       NewHistoryModel(),
		config:        NewConfigModel(),
		statusBar:     NewStatusBarModel(len(statuses)),
		runManager:    runManager,
	}
}

func (m MainModel) Init() tea.Cmd {
	return tea.Batch(
		m.logs.Init(),
		m.statusBar.Init(),
		m.listenForEvents(),
	)
}

func (m MainModel) listenForEvents() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-m.runManager.EventChan()
		if !ok {
			return nil
		}
		return event
	}
}

func (m MainModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "tab":
			m.activeTab = (m.activeTab + 1) % tabID(len(tabNames))
			return m, nil
		case "shift+tab":
			m.activeTab = (m.activeTab - 1 + tabID(len(tabNames))) % tabID(len(tabNames))
			return m, nil
		}

	case RunEvent:
		cmds = append(cmds, m.listenForEvents())
	}

	var cmd tea.Cmd

	m.subscriptions, cmd = m.subscriptions.Update(msg)
	cmds = append(cmds, cmd)

	m.logs, cmd = m.logs.Update(msg)
	cmds = append(cmds, cmd)

	m.history, cmd = m.history.Update(msg)
	cmds = append(cmds, cmd)

	m.config, cmd = m.config.Update(msg)
	cmds = append(cmds, cmd)

	m.statusBar, cmd = m.statusBar.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m MainModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	b.WriteString(m.renderTabBar())
	b.WriteString("\n")

	switch m.activeTab {
	case tabSubscriptions:
		b.WriteString(ContentStyle.Render(m.subscriptions.View()))
	case tabLogs:
		b.WriteString(ContentStyle.Render(m.logs.View()))
	case tabHistory:
		b.WriteString(ContentStyle.Render(m.history.View()))
	case tabConfig:
		b.WriteString(ContentStyle.Render(m.config.View()))
	}

	b.WriteString("\n")
	b.WriteString(m.statusBar.View())

	return b.String()
}

func (m MainModel) renderTabBar() string {
	var tabs []string
	for i, name := range tabNames {
		if tabID(i) == m.activeTab {
			tabs = append(tabs, ActiveTabStyle.Render(name))
		} else {
			tabs = append(tabs, InactiveTabStyle.Render(name))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

// Run starts the bubbletea program and the RunManager concurrently.
func Run(ctx context.Context, myLibrary *library.Library, runManager *RunManager, logWriter *DualWriter) error {
	model := NewMainModel(myLibrary, runManager, logWriter.LogChan())

	go runManager.RunAll(ctx)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
