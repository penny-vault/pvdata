package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/viper"
)

type ConfigModel struct {
	viewport viewport.Model
	ready    bool
}

func NewConfigModel() ConfigModel {
	return ConfigModel{}
}

func (m ConfigModel) Init() tea.Cmd {
	return nil
}

func (m ConfigModel) Update(msg tea.Msg) (ConfigModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport = viewport.New(viewport.WithWidth(msg.Width), viewport.WithHeight(msg.Height-6))
		m.ready = true
		m.viewport.SetContent(m.renderConfig())
	}

	if m.ready {
		var cmd tea.Cmd

		m.viewport, cmd = m.viewport.Update(msg)

		return m, cmd
	}

	return m, nil
}

func (m ConfigModel) View() string {
	if !m.ready {
		return "Initializing config..."
	}

	return m.viewport.View()
}

func (m ConfigModel) renderConfig() string {
	var b strings.Builder

	b.WriteString("-- Configuration --\n\n")

	keys := []struct {
		label string
		key   string
	}{
		{"Database URL", "db.url"},
		{"OpenFIGI API Key", "openfigi.apikey"},
		{"Web Port", "web.port"},
		{"Config File", ""},
	}

	for _, k := range keys {
		if k.key == "" {
			fmt.Fprintf(&b, "  %-25s %s\n", k.label+":", viper.ConfigFileUsed())
		} else {
			val := viper.GetString(k.key)
			if val == "" {
				val = "(not set)"
			}

			if strings.Contains(k.key, "apikey") && val != "(not set)" {
				if len(val) > 4 {
					val = val[:4] + strings.Repeat("*", len(val)-4)
				}
			}

			if strings.Contains(k.key, "url") && val != "(not set)" {
				val = maskDBPassword(val)
			}

			fmt.Fprintf(&b, "  %-25s %s\n", k.label+":", val)
		}
	}

	return b.String()
}

func maskDBPassword(url string) string {
	if idx := strings.Index(url, "://"); idx >= 0 {
		rest := url[idx+3:]
		if atIdx := strings.Index(rest, "@"); atIdx >= 0 {
			userPass := rest[:atIdx]
			if colonIdx := strings.Index(userPass, ":"); colonIdx >= 0 {
				return url[:idx+3] + userPass[:colonIdx+1] + "****" + "@" + rest[atIdx+1:]
			}
		}
	}

	return url
}
