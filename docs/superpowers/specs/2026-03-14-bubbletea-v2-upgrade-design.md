# Bubbletea v2 Upgrade Design

## Problem

The project uses charmbracelet bubbletea v1 (`github.com/charmbracelet/*` packages). The v2 release (`charm.land/*/v2`) is available with improved APIs. We need to upgrade to v2 before adding new TUI features (progress bars, etc.).

## Scope

Upgrade all charmbracelet dependencies from v1 to v2:

| Package | v1 Path | v2 Path |
|---|---|---|
| bubbletea | `github.com/charmbracelet/bubbletea v1.3.10` | `charm.land/bubbletea/v2` |
| bubbles | `github.com/charmbracelet/bubbles v1.0.0` | `charm.land/bubbles/v2` |
| lipgloss | `github.com/charmbracelet/lipgloss v1.1.1` | `charm.land/lipgloss/v2` |
| huh | `github.com/charmbracelet/huh v0.8.0` | `charm.land/huh/v2` |
| glamour | `github.com/charmbracelet/glamour v0.10.0` | `charm.land/glamour/v2` |

## Files affected

### tui/ (8 files)

| File | Charmbracelet packages used |
|---|---|
| `tui/tui.go` | bubbletea, lipgloss |
| `tui/config.go` | bubbletea, bubbles/viewport |
| `tui/history.go` | bubbletea, bubbles/viewport |
| `tui/logs.go` | bubbletea, bubbles/viewport |
| `tui/publish.go` | bubbletea, bubbles/table, bubbles/textinput, lipgloss |
| `tui/statusbar.go` | bubbletea |
| `tui/subscriptions.go` | bubbletea, bubbles/table, lipgloss |
| `tui/styles.go` | lipgloss |
| `tui/preflight.go` | huh |

### cmd/ (6 files)

| File | Charmbracelet packages used |
|---|---|
| `cmd/publish.go` | bubbletea |
| `cmd/subscribe.go` | huh, lipgloss |
| `cmd/unsubscribe.go` | huh |
| `cmd/init.go` | huh |
| `cmd/info.go` | glamour |
| `cmd/providers.go` | glamour |

## Breaking changes

### 1. Import paths

All `github.com/charmbracelet/*` imports become `charm.land/*/v2`:

- `github.com/charmbracelet/bubbletea` -> `charm.land/bubbletea/v2`
- `github.com/charmbracelet/bubbles/*` -> `charm.land/bubbles/v2/*`
- `github.com/charmbracelet/lipgloss` -> `charm.land/lipgloss/v2`
- `github.com/charmbracelet/huh` -> `charm.land/huh/v2`
- `github.com/charmbracelet/glamour` -> `charm.land/glamour/v2`

### 2. View() return type

All `View() string` methods become `View() tea.View`. Wrap return values with `tea.NewView()`:

```go
// v1
func (m Model) View() string {
    return "hello"
}

// v2
func (m Model) View() tea.View {
    return tea.NewView("hello")
}
```

Affected files: tui/tui.go, tui/config.go, tui/history.go, tui/logs.go, tui/publish.go, tui/statusbar.go, tui/subscriptions.go

### 3. KeyMsg -> KeyPressMsg

`tea.KeyMsg` type assertions become `tea.KeyPressMsg`. Key string comparisons may also change (e.g., `" "` becomes `"space"`).

Affected files: tui/tui.go, tui/publish.go

### 4. WithAltScreen moves to View

`tea.WithAltScreen()` is no longer a `NewProgram` option. Instead, set `AltScreen: true` on the `tea.View` return value.

```go
// v1
p := tea.NewProgram(model, tea.WithAltScreen())

// v2
func (m Model) View() tea.View {
    v := tea.NewView(content)
    v.AltScreen = true
    return v
}
```

Affected files: cmd/publish.go (launches TUI), and the View() of the model that uses alt screen

### 5. Viewport constructor

`viewport.New(width, height)` changes to `viewport.New()` with dimensions set via methods.

Affected files: tui/config.go, tui/history.go, tui/logs.go

### 6. Textinput width assignment

Direct field assignment `m.input.Width = 12` becomes `m.input.SetWidth(12)`.

Affected files: tui/publish.go

## Approach

Mechanical file-by-file migration. No behavioral changes -- the TUI should work identically after the upgrade.

### Steps

1. Update go.mod: `go get charm.land/bubbletea/v2 charm.land/bubbles/v2 charm.land/lipgloss/v2 charm.land/huh/v2 charm.land/glamour/v2`
2. Update imports in all 14 files
3. Fix View() signatures and return types
4. Fix KeyMsg -> KeyPressMsg
5. Fix WithAltScreen usage
6. Fix viewport constructors
7. Fix textinput width assignment
8. Remove old charmbracelet dependencies from go.mod
9. Build and verify

## Testing

- `go build ./...` -- must compile clean
- `go test ./...` -- all tests pass
- `golangci-lint run` -- no new lint issues
- Manual smoke test: `pvdata run`, `pvdata publish`, `pvdata subscribe` to verify TUI behavior is unchanged
