package cmd

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
)

// progressMsg is sent by the migration goroutine to report progress on a step.
type progressMsg struct {
	step    string // display name of the current step
	current int64  // rows copied so far in this step
	total   int64  // total rows for this step
	done    bool   // true when this step is complete
}

// migrationDoneMsg is sent when the entire migration finishes.
type migrationDoneMsg struct{}

type completedStep struct {
	name string
	rows int64
}

type migrationModel struct {
	progress    progress.Model
	progressCh  <-chan progressMsg
	currentStep string
	currentRows int64
	totalRows   int64
	completed   []completedStep
	done        bool
	width       int
}

const (
	progressPadding  = 2
	progressMaxWidth = 72
)

func newMigrationModel(ch <-chan progressMsg) migrationModel {
	return migrationModel{
		progress:   progress.New(progress.WithDefaultBlend()),
		progressCh: ch,
	}
}

func (m migrationModel) Init() tea.Cmd {
	return m.waitForProgress()
}

func (m migrationModel) waitForProgress() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.progressCh
		if !ok {
			return migrationDoneMsg{}
		}

		return msg
	}
}

func (m migrationModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width - progressPadding*2 - 4
		if m.width > progressMaxWidth {
			m.width = progressMaxWidth
		}

		m.progress.SetWidth(m.width)

		return m, nil

	case progressMsg:
		if msg.done {
			m.completed = append(m.completed, completedStep{
				name: msg.step,
				rows: msg.current,
			})
			m.currentStep = ""
			m.currentRows = 0
			m.totalRows = 0

			// Reset progress bar to 0 for next step
			cmd := m.progress.SetPercent(0)

			return m, tea.Batch(m.waitForProgress(), cmd)
		}

		m.currentStep = msg.step
		m.currentRows = msg.current
		m.totalRows = msg.total

		var pct float64
		if msg.total > 0 {
			pct = float64(msg.current) / float64(msg.total)
		}

		cmd := m.progress.SetPercent(pct)

		return m, tea.Batch(m.waitForProgress(), cmd)

	case migrationDoneMsg:
		m.done = true

		return m, tea.Quit

	case progress.FrameMsg:
		var cmd tea.Cmd

		m.progress, cmd = m.progress.Update(msg)

		return m, cmd

	default:
		return m, nil
	}
}

func (m migrationModel) View() tea.View {
	pad := strings.Repeat(" ", progressPadding)

	var b strings.Builder

	b.WriteString("\n" + pad + "Migrating legacy database...\n\n")

	// Completed steps
	for _, s := range m.completed {
		if s.rows > 0 {
			fmt.Fprintf(&b, "%s  [done] %-25s %s rows\n", pad, s.name, formatNumber(s.rows))
		} else {
			fmt.Fprintf(&b, "%s  [done] %s\n", pad, s.name)
		}
	}

	// Current step with progress bar
	if m.currentStep != "" && m.totalRows > 0 {
		fmt.Fprintf(&b, "%s  %-25s\n", pad, m.currentStep)
		b.WriteString(pad + "  " + m.progress.View() + "\n")
	} else if m.currentStep != "" {
		fmt.Fprintf(&b, "%s  %s...\n", pad, m.currentStep)
	}

	// Done message
	if m.done {
		b.WriteString("\n")
		b.WriteString(pad + "  Migration completed successfully.\n")
		b.WriteString("\n")
	}

	return tea.NewView(b.String())
}

func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	if n < 1_000_000 {
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	}

	return fmt.Sprintf("%d,%03d,%03d", n/1_000_000, (n%1_000_000)/1000, n%1000)
}
