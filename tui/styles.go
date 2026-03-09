package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Tab bar styles
	ActiveTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("57")).
			Padding(0, 2)

	InactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Padding(0, 2)

	TabGapStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("238"))

	// Status bar styles
	StatusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	// Content area
	ContentStyle = lipgloss.NewStyle().
			Padding(1, 2)

	// Log level colors
	LogDebug = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	LogInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	LogWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	LogError = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	// Help style
	HelpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
