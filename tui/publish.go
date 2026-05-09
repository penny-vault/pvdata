// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

type publishScreen int

const (
	screenList publishScreen = iota
	screenDetail
	screenAddSource
	screenEditBoundary
	screenConfirmRemove
	screenNewView
)

// candidateSource represents a subscription table that could be added to a view.
type candidateSource struct {
	TableName      string
	SubscriptionID string
	SubName        string
	Provider       string
	Dataset        string
}

// PublishModel is the top-level bubbletea model for managing published views.
type PublishModel struct {
	ctx     context.Context
	library *library.Library
	screen  publishScreen

	// List screen
	viewsTable table.Model
	views      []*library.PublishedView

	// Detail screen
	selectedView *library.PublishedView
	sourcesTable table.Model

	// Add source
	candidateTables []candidateSource
	addTable        table.Model

	// Edit boundary
	editingSourceIdx int
	fromInput        textinput.Model
	untilInput       textinput.Model
	editFocusFrom    bool

	// Remove
	removeSourceIdx int

	// New view
	newViewDataTypes []string
	newViewTable     table.Model

	// General
	width   int
	height  int
	message string
}

// NewPublishModel creates a new PublishModel, loading views from the database.
func NewPublishModel(ctx context.Context, lib *library.Library) PublishModel {
	m := PublishModel{
		ctx:     ctx,
		library: lib,
		screen:  screenList,
	}

	m.loadViews()
	m.viewsTable = m.buildViewsTable()

	return m
}

func (m *PublishModel) loadViews() {
	views, err := library.LoadPublishedViews(m.ctx, m.library.Pool)
	if err != nil {
		m.message = fmt.Sprintf("Error loading views: %v", err)
		m.views = nil

		return
	}

	m.views = views
}

func (m *PublishModel) buildViewsTable() table.Model {
	columns := []table.Column{
		{Title: "View Name", Width: 30},
		{Title: "Data Type", Width: 20},
		{Title: "Sources", Width: 10},
	}

	rows := make([]table.Row, 0, len(m.views))
	for _, v := range m.views {
		dtName := v.DataTypeKey
		if dt, ok := data.DataTypes[v.DataTypeKey]; ok {
			dtName = dt.Name
		}

		rows = append(rows, table.Row{
			v.ViewName,
			dtName,
			fmt.Sprintf("%d", len(v.Sources)),
		})
	}

	height := 15
	if m.height > 0 {
		height = max(m.height-8, 3)
	}

	width := 0
	for _, c := range columns {
		width += c.Width + 2
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(height),
		table.WithWidth(width),
	)
	t.SetStyles(tableStyles())

	return t
}

func (m *PublishModel) buildSourcesTable() table.Model {
	columns := []table.Column{
		{Title: "Source Table", Width: 35},
		{Title: "Subscription", Width: 15},
		{Title: "From", Width: 12},
		{Title: "Until", Width: 12},
	}

	rows := make([]table.Row, 0)

	if m.selectedView != nil {
		for _, s := range m.selectedView.Sources {
			fromStr := ""
			if s.FromDate != nil {
				fromStr = s.FromDate.Format("2006-01-02")
			}

			untilStr := ""
			if s.UntilDate != nil {
				untilStr = s.UntilDate.Format("2006-01-02")
			}

			subLabel := s.SubscriptionID
			if len(subLabel) > 12 {
				subLabel = subLabel[:12]
			}

			rows = append(rows, table.Row{
				s.TableName,
				subLabel,
				fromStr,
				untilStr,
			})
		}
	}

	height := 10
	if m.height > 0 {
		height = max(m.height-10, 3)
	}

	width := 0
	for _, c := range columns {
		width += c.Width + 2
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(height),
		table.WithWidth(width),
	)
	t.SetStyles(tableStyles())

	return t
}

func (m *PublishModel) buildAddTable() table.Model {
	columns := []table.Column{
		{Title: "Table Name", Width: 35},
		{Title: "Subscription", Width: 20},
		{Title: "Provider/Dataset", Width: 25},
	}

	rows := make([]table.Row, 0, len(m.candidateTables))
	for _, c := range m.candidateTables {
		rows = append(rows, table.Row{
			c.TableName,
			c.SubName,
			fmt.Sprintf("%s/%s", c.Provider, c.Dataset),
		})
	}

	height := 10
	if m.height > 0 {
		height = max(m.height-10, 3)
	}

	width := 0
	for _, c := range columns {
		width += c.Width + 2
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(height),
		table.WithWidth(width),
	)
	t.SetStyles(tableStyles())

	return t
}

func (m *PublishModel) buildNewViewTable() table.Model {
	columns := []table.Column{
		{Title: "Data Type", Width: 25},
		{Title: "View Name", Width: 30},
	}

	rows := make([]table.Row, 0, len(m.newViewDataTypes))
	for _, dtKey := range m.newViewDataTypes {
		dt := data.DataTypes[dtKey]
		viewName := dt.ViewName
		rows = append(rows, table.Row{
			dt.Name,
			viewName,
		})
	}

	height := 10
	if m.height > 0 {
		height = max(m.height-10, 3)
	}

	width := 0
	for _, c := range columns {
		width += c.Width + 2
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(height),
		table.WithWidth(width),
	)
	t.SetStyles(tableStyles())

	return t
}

func tableStyles() table.Styles {
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

	return s
}

// Init implements tea.Model.
func (m PublishModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m PublishModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Clear message on any key press
	if _, ok := msg.(tea.KeyPressMsg); ok && m.message != "" {
		m.message = ""
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		m.viewsTable = m.buildViewsTable()
		if m.selectedView != nil {
			m.sourcesTable = m.buildSourcesTable()
		}

		return m, nil
	}

	switch m.screen {
	case screenList:
		return m.updateList(msg)
	case screenDetail:
		return m.updateDetail(msg)
	case screenAddSource:
		return m.updateAddSource(msg)
	case screenEditBoundary:
		return m.updateEditBoundary(msg)
	case screenConfirmRemove:
		return m.updateConfirmRemove(msg)
	case screenNewView:
		return m.updateNewView(msg)
	}

	return m, nil
}

func (m PublishModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter", " ":
			idx := m.viewsTable.Cursor()
			if idx >= 0 && idx < len(m.views) {
				m.selectedView = m.views[idx]
				m.sourcesTable = m.buildSourcesTable()
				m.screen = screenDetail
			}

			return m, nil
		case "n":
			m.prepareNewView()

			if len(m.newViewDataTypes) == 0 {
				m.message = "No data types available for new views (all already have views)."
				return m, nil
			}

			m.screen = screenNewView

			return m, nil
		case "r":
			m.loadViews()
			m.viewsTable = m.buildViewsTable()
			m.message = "Views refreshed."

			return m, nil
		}
	}

	var cmd tea.Cmd

	m.viewsTable, cmd = m.viewsTable.Update(msg)

	return m, cmd
}

func (m PublishModel) updateDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace":
			m.screen = screenList
			m.selectedView = nil
			m.loadViews()
			m.viewsTable = m.buildViewsTable()

			return m, nil
		case "a":
			m.prepareCandidates()

			if len(m.candidateTables) == 0 {
				m.message = "No candidate tables available for this data type."
				return m, nil
			}

			m.addTable = m.buildAddTable()
			m.screen = screenAddSource

			return m, nil
		case "e":
			idx := m.sourcesTable.Cursor()
			if idx >= 0 && idx < len(m.selectedView.Sources) {
				m.editingSourceIdx = idx
				m.prepareEditInputs(idx)
				m.screen = screenEditBoundary

				return m, textinput.Blink
			}

			return m, nil
		case "d", "x":
			idx := m.sourcesTable.Cursor()
			if idx >= 0 && idx < len(m.selectedView.Sources) {
				m.removeSourceIdx = idx
				m.screen = screenConfirmRemove
			}

			return m, nil
		}
	}

	var cmd tea.Cmd

	m.sourcesTable, cmd = m.sourcesTable.Update(msg)

	return m, cmd
}

func (m PublishModel) updateAddSource(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.screen = screenDetail
			return m, nil
		case "enter":
			idx := m.addTable.Cursor()
			if idx >= 0 && idx < len(m.candidateTables) {
				candidate := m.candidateTables[idx]

				m.selectedView.Sources = append(m.selectedView.Sources, library.ViewSource{
					TableName:      candidate.TableName,
					SubscriptionID: candidate.SubscriptionID,
				})
				if err := library.SavePublishedView(m.ctx, m.library.Pool, m.selectedView); err != nil {
					m.message = fmt.Sprintf("Error saving view: %v", err)
					// Roll back the in-memory change
					m.selectedView.Sources = m.selectedView.Sources[:len(m.selectedView.Sources)-1]
				} else {
					m.message = fmt.Sprintf("Added source %s.", candidate.TableName)
				}

				m.sourcesTable = m.buildSourcesTable()
				m.screen = screenDetail
			}

			return m, nil
		}
	}

	var cmd tea.Cmd

	m.addTable, cmd = m.addTable.Update(msg)

	return m, cmd
}

func (m PublishModel) updateEditBoundary(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.screen = screenDetail
			return m, nil
		case "tab", "shift+tab":
			m.editFocusFrom = !m.editFocusFrom
			if m.editFocusFrom {
				m.fromInput.Focus()
				m.untilInput.Blur()
			} else {
				m.fromInput.Blur()
				m.untilInput.Focus()
			}

			return m, textinput.Blink
		case "enter":
			fromStr := strings.TrimSpace(m.fromInput.Value())
			untilStr := strings.TrimSpace(m.untilInput.Value())

			source := &m.selectedView.Sources[m.editingSourceIdx]
			oldFrom := source.FromDate
			oldUntil := source.UntilDate

			if fromStr == "" {
				source.FromDate = nil
			} else {
				t, err := time.Parse("2006-01-02", fromStr)
				if err != nil {
					m.message = fmt.Sprintf("Invalid from date: %v", err)
					return m, nil
				}

				source.FromDate = &t
			}

			if untilStr == "" {
				source.UntilDate = nil
			} else {
				t, err := time.Parse("2006-01-02", untilStr)
				if err != nil {
					m.message = fmt.Sprintf("Invalid until date: %v", err)
					return m, nil
				}

				source.UntilDate = &t
			}

			if err := library.SavePublishedView(m.ctx, m.library.Pool, m.selectedView); err != nil {
				m.message = fmt.Sprintf("Error saving view: %v", err)
				source.FromDate = oldFrom
				source.UntilDate = oldUntil
			} else {
				m.message = "Dates updated."
			}

			m.sourcesTable = m.buildSourcesTable()
			m.screen = screenDetail

			return m, nil
		}
	}

	var cmd tea.Cmd
	if m.editFocusFrom {
		m.fromInput, cmd = m.fromInput.Update(msg)
	} else {
		m.untilInput, cmd = m.untilInput.Update(msg)
	}

	return m, cmd
}

func (m PublishModel) updateConfirmRemove(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "y", "Y":
			idx := m.removeSourceIdx
			tableName := m.selectedView.Sources[idx].TableName
			oldSources := make([]library.ViewSource, len(m.selectedView.Sources))
			copy(oldSources, m.selectedView.Sources)

			m.selectedView.Sources = append(
				m.selectedView.Sources[:idx],
				m.selectedView.Sources[idx+1:]...,
			)

			if len(m.selectedView.Sources) == 0 {
				// Delete the entire view
				viewName := m.selectedView.ViewName
				if err := library.DeletePublishedView(m.ctx, m.library.Pool, viewName); err != nil {
					m.message = fmt.Sprintf("Error deleting view: %v", err)
					m.selectedView.Sources = oldSources
					m.screen = screenDetail
				} else {
					m.message = fmt.Sprintf("Deleted view %s (last source %s removed).", viewName, tableName)
					m.selectedView = nil
					m.loadViews()
					m.viewsTable = m.buildViewsTable()
					m.screen = screenList
				}
			} else {
				if err := library.SavePublishedView(m.ctx, m.library.Pool, m.selectedView); err != nil {
					m.message = fmt.Sprintf("Error saving view: %v", err)
					m.selectedView.Sources = oldSources
				} else {
					m.message = fmt.Sprintf("Removed source %s.", tableName)
				}

				m.sourcesTable = m.buildSourcesTable()
				m.screen = screenDetail
			}

			return m, nil
		case "n", "N", "esc":
			m.screen = screenDetail
			return m, nil
		}
	}

	return m, nil
}

func (m PublishModel) updateNewView(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.screen = screenList
			return m, nil
		case "enter":
			idx := m.newViewTable.Cursor()
			if idx >= 0 && idx < len(m.newViewDataTypes) {
				dtKey := m.newViewDataTypes[idx]
				dt := data.DataTypes[dtKey]

				pv := &library.PublishedView{
					ViewName:    dt.ViewName,
					DataTypeKey: dtKey,
					Sources:     []library.ViewSource{},
				}
				if err := library.SavePublishedView(m.ctx, m.library.Pool, pv); err != nil {
					m.message = fmt.Sprintf("Error creating view: %v", err)
					m.screen = screenList
				} else {
					m.loadViews()
					m.viewsTable = m.buildViewsTable()
					// Select the newly created view and go to detail
					for i, v := range m.views {
						if v.ViewName == dt.ViewName {
							m.selectedView = m.views[i]
							break
						}
					}

					if m.selectedView != nil {
						m.sourcesTable = m.buildSourcesTable()
						m.screen = screenDetail
						m.message = fmt.Sprintf("Created view %s. Add sources with 'a'.", dt.ViewName)
					} else {
						m.screen = screenList
						m.message = fmt.Sprintf("Created view %s.", dt.ViewName)
					}
				}
			}

			return m, nil
		}
	}

	var cmd tea.Cmd

	m.newViewTable, cmd = m.newViewTable.Update(msg)

	return m, cmd
}

// View implements tea.Model.
func (m PublishModel) View() tea.View {
	var b strings.Builder

	switch m.screen {
	case screenList:
		b.WriteString(m.viewList())
	case screenDetail:
		b.WriteString(m.viewDetail())
	case screenAddSource:
		b.WriteString(m.viewAddSource())
	case screenEditBoundary:
		b.WriteString(m.viewEditBoundary())
	case screenConfirmRemove:
		b.WriteString(m.viewConfirmRemove())
	case screenNewView:
		b.WriteString(m.viewNewView())
	}

	v := tea.NewView(b.String())
	v.AltScreen = true

	return v
}

func (m PublishModel) viewList() string {
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).Padding(1, 2).Render("Published Views")
	b.WriteString(title)
	b.WriteString("\n")

	b.WriteString(ContentStyle.Render(m.viewsTable.View()))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString(LogError.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString(HelpStyle.Render("  enter: view details | n: new view | r: refresh | q: quit"))
	b.WriteString("\n")

	return b.String()
}

func (m PublishModel) viewDetail() string {
	var b strings.Builder

	viewName := ""
	dtName := ""

	if m.selectedView != nil {
		viewName = m.selectedView.ViewName

		dtName = m.selectedView.DataTypeKey
		if dt, ok := data.DataTypes[m.selectedView.DataTypeKey]; ok {
			dtName = dt.Name
		}
	}

	title := lipgloss.NewStyle().Bold(true).Padding(1, 2).
		Render(fmt.Sprintf("View: %s  [%s]", viewName, dtName))
	b.WriteString(title)
	b.WriteString("\n")

	b.WriteString(ContentStyle.Render(m.sourcesTable.View()))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString(LogError.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString(HelpStyle.Render("  a: add source | e: edit dates | d: remove source | esc: back | q: quit"))
	b.WriteString("\n")

	return b.String()
}

func (m PublishModel) viewAddSource() string {
	var b strings.Builder

	viewName := ""
	if m.selectedView != nil {
		viewName = m.selectedView.ViewName
	}

	title := lipgloss.NewStyle().Bold(true).Padding(1, 2).
		Render(fmt.Sprintf("Add Source to: %s", viewName))
	b.WriteString(title)
	b.WriteString("\n")

	b.WriteString(ContentStyle.Render(m.addTable.View()))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString(LogError.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString(HelpStyle.Render("  enter: add selected | esc: cancel"))
	b.WriteString("\n")

	return b.String()
}

func (m PublishModel) viewEditBoundary() string {
	var b strings.Builder

	sourceName := ""
	if m.editingSourceIdx < len(m.selectedView.Sources) {
		sourceName = m.selectedView.Sources[m.editingSourceIdx].TableName
	}

	title := lipgloss.NewStyle().Bold(true).Padding(1, 2).
		Render(fmt.Sprintf("Edit Date Bounds: %s", sourceName))
	b.WriteString(title)
	b.WriteString("\n\n")

	fromLabel := "  From date (YYYY-MM-DD or empty): "
	untilLabel := "  Until date (YYYY-MM-DD or empty): "

	if m.editFocusFrom {
		b.WriteString(ActiveTabStyle.Render(fromLabel))
	} else {
		b.WriteString(InactiveTabStyle.Render(fromLabel))
	}

	b.WriteString(m.fromInput.View())
	b.WriteString("\n\n")

	if !m.editFocusFrom {
		b.WriteString(ActiveTabStyle.Render(untilLabel))
	} else {
		b.WriteString(InactiveTabStyle.Render(untilLabel))
	}

	b.WriteString(m.untilInput.View())
	b.WriteString("\n\n")

	if m.message != "" {
		b.WriteString(LogError.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString(HelpStyle.Render("  tab: switch field | enter: save | esc: cancel"))
	b.WriteString("\n")

	return b.String()
}

func (m PublishModel) viewConfirmRemove() string {
	var b strings.Builder

	tableName := ""
	viewName := ""

	if m.selectedView != nil && m.removeSourceIdx < len(m.selectedView.Sources) {
		tableName = m.selectedView.Sources[m.removeSourceIdx].TableName
		viewName = m.selectedView.ViewName
	}

	b.WriteString("\n\n")

	prompt := fmt.Sprintf("  Remove source %s from %s?", tableName, viewName)
	if m.selectedView != nil && len(m.selectedView.Sources) == 1 {
		prompt += "\n  (This is the last source -- the entire view will be deleted.)"
	}

	b.WriteString(LogWarn.Render(prompt))
	b.WriteString("\n\n")
	b.WriteString(HelpStyle.Render("  y: confirm | n/esc: cancel"))
	b.WriteString("\n")

	return b.String()
}

func (m PublishModel) viewNewView() string {
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).Padding(1, 2).
		Render("Create New Published View -- Select Data Type")
	b.WriteString(title)
	b.WriteString("\n")

	b.WriteString(ContentStyle.Render(m.newViewTable.View()))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString(LogError.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString(HelpStyle.Render("  enter: create view | esc: cancel"))
	b.WriteString("\n")

	return b.String()
}

// prepareEditInputs sets up text inputs for editing date boundaries.
func (m *PublishModel) prepareEditInputs(idx int) {
	source := m.selectedView.Sources[idx]

	m.fromInput = textinput.New()
	m.fromInput.Placeholder = "YYYY-MM-DD"
	m.fromInput.CharLimit = 10
	m.fromInput.SetWidth(12)

	if source.FromDate != nil {
		m.fromInput.SetValue(source.FromDate.Format("2006-01-02"))
	}

	m.untilInput = textinput.New()
	m.untilInput.Placeholder = "YYYY-MM-DD"
	m.untilInput.CharLimit = 10
	m.untilInput.SetWidth(12)

	if source.UntilDate != nil {
		m.untilInput.SetValue(source.UntilDate.Format("2006-01-02"))
	}

	m.editFocusFrom = true
	m.fromInput.Focus()
}

// prepareCandidates builds the list of subscription tables matching the view's
// data type that are not already sources.
func (m *PublishModel) prepareCandidates() {
	m.candidateTables = nil

	if m.selectedView == nil {
		return
	}

	subs, err := m.library.Subscriptions(m.ctx)
	if err != nil {
		m.message = fmt.Sprintf("Error loading subscriptions: %v", err)
		return
	}

	// Build set of existing source table names
	existing := make(map[string]bool)
	for _, s := range m.selectedView.Sources {
		existing[s.TableName] = true
	}

	dtKey := m.selectedView.DataTypeKey

	for _, sub := range subs {
		tableName, ok := sub.DataTablesMap[dtKey]
		if !ok || tableName == "" {
			continue
		}

		if existing[tableName] {
			continue
		}

		m.candidateTables = append(m.candidateTables, candidateSource{
			TableName:      tableName,
			SubscriptionID: sub.ID.String(),
			SubName:        sub.Name,
			Provider:       sub.Provider,
			Dataset:        sub.Dataset,
		})
	}
}

// prepareNewView builds the list of data types that do not already have a published view.
func (m *PublishModel) prepareNewView() {
	m.newViewDataTypes = nil

	existingViewNames := make(map[string]bool)
	for _, v := range m.views {
		existingViewNames[v.ViewName] = true
	}

	for dtKey, dt := range data.DataTypes {
		if dt.ViewName == "" {
			continue
		}

		// Published views are Postgres-only; non-PG backends store data
		// outside the SQL view universe entirely.
		if dt.Backend != data.BackendPostgres {
			continue
		}

		if existingViewNames[dt.ViewName] {
			continue
		}

		m.newViewDataTypes = append(m.newViewDataTypes, dtKey)
	}

	sort.Strings(m.newViewDataTypes)
	m.newViewTable = m.buildNewViewTable()
}
