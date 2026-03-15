package cmd

import (
	"fmt"
	"strings"
	"time"

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
type migrationDoneMsg struct {
	err error
}

type completedStep struct {
	name string
	rows int64
}

type migrationModel struct {
	progress    progress.Model
	progressCh  <-chan progressMsg
	doneCh      <-chan error
	currentStep string
	currentRows int64
	totalRows   int64
	stepStart   time.Time
	startTime   time.Time
	completed   []completedStep
	err         error
	done        bool
	width       int
}

const (
	progressPadding  = 2
	progressMaxWidth = 72
)

func newMigrationModel(ch <-chan progressMsg, done <-chan error) migrationModel {
	return migrationModel{
		progress:   progress.New(progress.WithDefaultBlend()),
		progressCh: ch,
		doneCh:     done,
		startTime:  time.Now(),
	}
}

type tickMsg time.Time

func (m migrationModel) Init() tea.Cmd {
	return tea.Batch(m.waitForProgress(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m migrationModel) waitForProgress() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-m.progressCh:
			if !ok {
				// Channel closed -- read the error from doneCh
				err := <-m.doneCh

				return migrationDoneMsg{err: err}
			}

			return msg
		case err := <-m.doneCh:
			return migrationDoneMsg{err: err}
		}
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

		if m.currentStep != msg.step {
			m.stepStart = time.Now()
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
		m.err = msg.err

		return m, tea.Quit

	case tickMsg:
		// Re-render to update ETA display
		return m, tickCmd()

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

	elapsed := time.Since(m.startTime).Truncate(time.Second)

	fmt.Fprintf(&b, "\n%sMigrating legacy database... (%s elapsed)\n\n", pad, elapsed)

	// Completed steps
	for _, s := range m.completed {
		if s.rows > 0 {
			fmt.Fprintf(&b, "%s  [done] %-25s %s rows\n", pad, s.name, formatNumber(s.rows))
		} else {
			fmt.Fprintf(&b, "%s  [done] %s\n", pad, s.name)
		}
	}

	// Current step with progress bar and ETA
	if m.currentStep != "" && m.totalRows > 0 {
		eta := m.estimateRemaining()
		if eta != "" {
			fmt.Fprintf(&b, "%s  %-25s %s/%s rows  ETA: %s\n", pad, m.currentStep, formatNumber(m.currentRows), formatNumber(m.totalRows), eta)
		} else {
			fmt.Fprintf(&b, "%s  %-25s %s/%s rows\n", pad, m.currentStep, formatNumber(m.currentRows), formatNumber(m.totalRows))
		}

		b.WriteString(pad + "  " + m.progress.View() + "\n")
	} else if m.currentStep != "" {
		fmt.Fprintf(&b, "%s  %s...\n", pad, m.currentStep)
	}

	// Done message
	if m.done {
		b.WriteString("\n")

		if m.err != nil {
			fmt.Fprintf(&b, "%s  Migration failed: %v\n", pad, m.err)
		} else {
			b.WriteString(pad + "  Migration completed successfully.\n")
		}

		b.WriteString("\n")
	}

	return tea.NewView(b.String())
}

func (m migrationModel) estimateRemaining() string {
	if m.currentRows <= 0 || m.totalRows <= 0 || m.stepStart.IsZero() {
		return ""
	}

	elapsed := time.Since(m.stepStart).Seconds()
	if elapsed < 2 {
		return ""
	}

	rate := float64(m.currentRows) / elapsed
	remaining := float64(m.totalRows-m.currentRows) / rate
	eta := time.Duration(remaining) * time.Second

	if eta < time.Minute {
		return fmt.Sprintf("%ds", int(eta.Seconds()))
	}

	return fmt.Sprintf("%dm%ds", int(eta.Minutes()), int(eta.Seconds())%60)
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
