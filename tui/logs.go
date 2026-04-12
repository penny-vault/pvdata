package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// LogLineMsg is sent when a new log line arrives.
type LogLineMsg struct {
	Line string
}

// LogsModel is the sub-model for the Logs tab.
type LogsModel struct {
	viewport viewport.Model
	lines    []string
	maxLines int
	ready    bool
	logCh    <-chan LogEntry
}

func NewLogsModel(logCh <-chan LogEntry) LogsModel {
	return LogsModel{
		lines:    make([]string, 0, 1000),
		maxLines: 10000,
		logCh:    logCh,
	}
}

// WaitForLog returns a command that waits for the next log entry.
func (m LogsModel) WaitForLog() tea.Cmd {
	return func() tea.Msg {
		entry, ok := <-m.logCh
		if !ok {
			return nil
		}

		return LogLineMsg(entry)
	}
}

func (m LogsModel) Init() tea.Cmd {
	return m.WaitForLog()
}

func (m LogsModel) Update(msg tea.Msg) (LogsModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Subtract 4 from width to account for ContentStyle padding (2 left + 2 right)
		m.viewport = viewport.New(viewport.WithWidth(msg.Width-4), viewport.WithHeight(msg.Height-6))
		m.viewport.SoftWrap = true
		m.viewport.SetContent(strings.Join(m.lines, ""))
		m.ready = true

	case LogLineMsg:
		m.lines = append(m.lines, msg.Line)
		if len(m.lines) > m.maxLines {
			m.lines = m.lines[len(m.lines)-m.maxLines:]
		}

		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, ""))
			m.viewport.GotoBottom()
		}

		cmds = append(cmds, m.WaitForLog())
	}

	if m.ready {
		var cmd tea.Cmd

		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m LogsModel) View() string {
	if !m.ready {
		return "Initializing logs..."
	}

	return m.viewport.View()
}
