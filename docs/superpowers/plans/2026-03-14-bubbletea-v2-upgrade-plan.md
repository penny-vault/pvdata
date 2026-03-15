# Bubbletea v2 Upgrade Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade all charmbracelet dependencies from v1 (github.com paths) to v2 (charm.land paths).

**Architecture:** Mechanical file-by-file migration. Update go.mod first, then fix each file's imports and API usages. No behavioral changes.

**Tech Stack:** charm.land/bubbletea/v2, charm.land/bubbles/v2, charm.land/lipgloss/v2, charm.land/huh/v2, charm.land/glamour/v2

**Spec:** `docs/superpowers/specs/2026-03-14-bubbletea-v2-upgrade-design.md`

---

## File Structure

All modifications to existing files. No new files created.

| File | Changes |
|---|---|
| `go.mod` | Replace charmbracelet deps with charm.land v2 |
| `tui/tui.go` | Imports, View() signature, KeyMsg, WithAltScreen, NewProgram |
| `tui/config.go` | Imports, View() signature, viewport.New() |
| `tui/history.go` | Imports, View() signature, viewport.New() |
| `tui/logs.go` | Imports, View() signature, viewport.New() |
| `tui/publish.go` | Imports, View() signature, KeyMsg (7 places), textinput width, textinput.Blink |
| `tui/statusbar.go` | Imports, View() signature |
| `tui/subscriptions.go` | Imports, View() signature |
| `tui/styles.go` | Imports (lipgloss only) |
| `tui/preflight.go` | Imports (huh only) |
| `cmd/publish.go` | Imports, NewProgram/WithAltScreen |
| `cmd/subscribe.go` | Imports (huh, lipgloss) |
| `cmd/unsubscribe.go` | Imports (huh only) |
| `cmd/init.go` | Imports (huh only) |
| `cmd/info.go` | Imports (glamour only) |
| `cmd/providers.go` | Imports (glamour only) |

---

## Chunk 1: Dependencies and Core TUI

### Task 1: Update go.mod dependencies

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add v2 dependencies and remove v1**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data
go get charm.land/bubbletea/v2@latest
go get charm.land/bubbles/v2@latest
go get charm.land/lipgloss/v2@latest
go get charm.land/huh/v2@latest
go get charm.land/glamour/v2@latest
```

Do NOT run `go mod tidy` yet -- the code still imports the old paths, so tidy would remove the new deps. We'll tidy after all files are updated.

- [ ] **Step 2: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add charm.land v2 dependencies"
```

---

### Task 2: Migrate tui/styles.go (lipgloss only)

**Files:**
- Modify: `tui/styles.go`

This is the simplest file -- only a lipgloss import.

- [ ] **Step 1: Update import**

Change line 3:
```go
// FROM:
import "github.com/charmbracelet/lipgloss"
// TO:
import "charm.land/lipgloss/v2"
```

- [ ] **Step 2: Verify build of this file**

The lipgloss v2 API for `lipgloss.NewStyle()`, `.Foreground()`, `.Background()`, `.Bold()`, `.Render()` etc. is compatible. Check if there are any compilation errors after the import change. If `lipgloss.Color()` or `lipgloss.AdaptiveColor()` have changed, fix accordingly.

- [ ] **Step 3: Commit**

```bash
git add tui/styles.go
git commit -m "refactor: migrate tui/styles.go to lipgloss v2"
```

---

### Task 3: Migrate tui/statusbar.go (simplest bubbletea model)

**Files:**
- Modify: `tui/statusbar.go`

- [ ] **Step 1: Update import (line 7)**

```go
// FROM:
tea "github.com/charmbracelet/bubbletea"
// TO:
tea "charm.land/bubbletea/v2"
```

- [ ] **Step 2: Update View() signature (line 67)**

```go
// FROM:
func (m StatusBarModel) View() string {
// TO:
func (m StatusBarModel) View() tea.View {
```

Wrap the return value(s) with `tea.NewView()`. Find every `return` in the View() method and wrap the string with `tea.NewView(...)`.

- [ ] **Step 3: Check WindowSizeMsg (line 42)**

`tea.WindowSizeMsg` is still named the same in v2 but verify the fields `msg.Width` and `msg.Height` are still valid. If the struct changed, update accordingly.

- [ ] **Step 4: Commit**

```bash
git add tui/statusbar.go
git commit -m "refactor: migrate tui/statusbar.go to bubbletea v2"
```

---

### Task 4: Migrate tui/config.go, tui/history.go, tui/logs.go (viewport models)

These three files have identical migration patterns: bubbletea imports, View() signature, and viewport.New() constructor.

**Files:**
- Modify: `tui/config.go`
- Modify: `tui/history.go`
- Modify: `tui/logs.go`

- [ ] **Step 1: Update imports in all three files**

In each file, change:
```go
// FROM:
"github.com/charmbracelet/bubbles/viewport"
tea "github.com/charmbracelet/bubbletea"
// TO:
"charm.land/bubbles/v2/viewport"
tea "charm.land/bubbletea/v2"
```

- [ ] **Step 2: Update View() signatures in all three files**

```go
// FROM:
func (m ConfigModel) View() string {
func (m HistoryModel) View() string {
func (m LogsModel) View() string {
// TO:
func (m ConfigModel) View() tea.View {
func (m HistoryModel) View() tea.View {
func (m LogsModel) View() tea.View {
```

Wrap return values with `tea.NewView(...)`.

- [ ] **Step 3: Fix viewport.New() constructor**

In bubbletea v2, the viewport constructor may have changed. Check the v2 API:

```go
// v1:
m.viewport = viewport.New(msg.Width, msg.Height-6)
// v2 (if constructor changed):
m.viewport = viewport.New()
m.viewport.SetWidth(msg.Width)
m.viewport.SetHeight(msg.Height - 6)
```

Check the actual v2 API for `viewport.New()`. If it still accepts width/height arguments, keep the current call. If it changed to a zero-args constructor with setter methods, use those. The viewport's `SetContent()`, `Update()`, and `View()` methods should remain compatible.

- [ ] **Step 4: Commit**

```bash
git add tui/config.go tui/history.go tui/logs.go
git commit -m "refactor: migrate viewport models to bubbletea/bubbles v2"
```

---

### Task 5: Migrate tui/subscriptions.go (table model)

**Files:**
- Modify: `tui/subscriptions.go`

- [ ] **Step 1: Update imports (lines 7-9)**

```go
// FROM:
"github.com/charmbracelet/bubbles/table"
tea "github.com/charmbracelet/bubbletea"
"github.com/charmbracelet/lipgloss"
// TO:
"charm.land/bubbles/v2/table"
tea "charm.land/bubbletea/v2"
"charm.land/lipgloss/v2"
```

- [ ] **Step 2: Update View() signature (line 98)**

```go
// FROM:
func (m SubscriptionsModel) View() string {
// TO:
func (m SubscriptionsModel) View() tea.View {
```

Wrap return values with `tea.NewView(...)`.

- [ ] **Step 3: Verify table API compatibility**

The `table.New()` constructor with `table.WithColumns()`, `table.WithRows()`, `table.WithHeight()`, `table.WithStyles()` options should be compatible with v2. Verify `m.table.Update()`, `m.table.View()`, `m.table.SetRows()`, `m.table.SelectedRow()` still work.

- [ ] **Step 4: Commit**

```bash
git add tui/subscriptions.go
git commit -m "refactor: migrate tui/subscriptions.go to bubbletea/bubbles v2"
```

---

### Task 6: Migrate tui/publish.go (most complex TUI file)

**Files:**
- Modify: `tui/publish.go`

This is the largest migration -- it uses bubbletea, bubbles/table, bubbles/textinput, and lipgloss with many KeyMsg assertions.

- [ ] **Step 1: Update imports (lines 24-27)**

```go
// FROM:
"github.com/charmbracelet/bubbles/table"
"github.com/charmbracelet/bubbles/textinput"
tea "github.com/charmbracelet/bubbletea"
"github.com/charmbracelet/lipgloss"
// TO:
"charm.land/bubbles/v2/table"
"charm.land/bubbles/v2/textinput"
tea "charm.land/bubbletea/v2"
"charm.land/lipgloss/v2"
```

- [ ] **Step 2: Update View() signature (line 650)**

```go
// FROM:
func (m PublishModel) View() string {
// TO:
func (m PublishModel) View() tea.View {
```

Wrap return values with `tea.NewView(...)`.

- [ ] **Step 3: Update all tea.KeyMsg to tea.KeyPressMsg**

Change all 7 `tea.KeyMsg` type assertions:

```go
// FROM (lines 291, 328, 370, 424, 465, 542, 595):
case tea.KeyMsg:
// and:
if _, ok := msg.(tea.KeyMsg); ok && m.message != "" {

// TO:
case tea.KeyPressMsg:
// and:
if _, ok := msg.(tea.KeyPressMsg); ok && m.message != "" {
```

Also check key string comparisons. In v2, key names may differ:
- `msg.String() == " "` may need to become `msg.String() == "space"`
- Check each `case` block for key comparisons and update if needed

- [ ] **Step 4: Fix textinput width assignments (lines 854, 863)**

```go
// FROM:
m.fromInput.Width = 12
m.untilInput.Width = 12
// TO:
m.fromInput.SetWidth(12)
m.untilInput.SetWidth(12)
```

If `SetWidth()` doesn't exist in v2, check the actual API and use the correct method.

- [ ] **Step 5: Fix textinput.Blink references (lines 400, 482)**

Check if `textinput.Blink` still exists in v2. If it was removed or renamed:

```go
// FROM:
return m, textinput.Blink
// TO (if changed):
return m, m.fromInput.Focus()  // or whatever the v2 equivalent is
```

If `textinput.Blink` still exists in v2, leave it as-is.

- [ ] **Step 6: Commit**

```bash
git add tui/publish.go
git commit -m "refactor: migrate tui/publish.go to bubbletea/bubbles v2"
```

---

### Task 7: Migrate tui/tui.go (main model + program entry)

**Files:**
- Modify: `tui/tui.go`

- [ ] **Step 1: Update imports (lines 8-9)**

```go
// FROM:
tea "github.com/charmbracelet/bubbletea"
"github.com/charmbracelet/lipgloss"
// TO:
tea "charm.land/bubbletea/v2"
"charm.land/lipgloss/v2"
```

- [ ] **Step 2: Update View() signature (line 147)**

```go
// FROM:
func (m MainModel) View() string {
// TO:
func (m MainModel) View() tea.View {
```

Wrap return values with `tea.NewView(...)`.

Since this is the top-level model that uses `tea.WithAltScreen()`, set `AltScreen: true` on the returned View:

```go
func (m MainModel) View() tea.View {
    // ... existing view building logic ...
    v := tea.NewView(content)
    v.AltScreen = true
    return v
}
```

- [ ] **Step 3: Update KeyMsg (line 77)**

```go
// FROM:
case tea.KeyMsg:
// TO:
case tea.KeyPressMsg:
```

Check key string comparisons within this case block.

- [ ] **Step 4: Update NewProgram call (line 202)**

Remove `tea.WithAltScreen()` from the program creation since it's now on the View:

```go
// FROM:
p := tea.NewProgram(model, tea.WithAltScreen())
// TO:
p := tea.NewProgram(model)
```

Note: `tea.WithAltScreen()` is now controlled by the View's `AltScreen` field (done in Step 2).

- [ ] **Step 5: Verify sub-model View() calls compile**

The MainModel's `View()` calls `m.subscriptions.View()`, `m.logs.View()`, etc. These now return `tea.View` instead of `string`. The MainModel needs to call `.String()` or similar on these if it concatenates them. Check the v2 API for how to extract the string from a `tea.View`.

If sub-model views need to be composed into the main view, you may need to use the view's content. Check if `tea.View` has a method to get the rendered string, or if sub-models that are embedded in a parent should return `string` from a different method (like `ViewString()`).

- [ ] **Step 6: Commit**

```bash
git add tui/tui.go
git commit -m "refactor: migrate tui/tui.go to bubbletea v2"
```

---

## Chunk 2: Command Files and Cleanup

### Task 8: Migrate tui/preflight.go (huh only)

**Files:**
- Modify: `tui/preflight.go`

- [ ] **Step 1: Update import (line 7)**

```go
// FROM:
"github.com/charmbracelet/huh"
// TO:
"charm.land/huh/v2"
```

- [ ] **Step 2: Verify huh API compatibility**

Check that these patterns still work in huh v2:
- `huh.NewForm()`, `huh.NewGroup()`, `huh.NewSelect[string]()`, `huh.NewMultiSelect[string]()`
- `huh.NewOption[string](label, value)` or `huh.NewOption(label, value)`
- `form.Run()`
- `.Title()`, `.Options()`, `.Value()` builder methods

Fix any API changes.

- [ ] **Step 3: Commit**

```bash
git add tui/preflight.go
git commit -m "refactor: migrate tui/preflight.go to huh v2"
```

---

### Task 9: Migrate cmd/publish.go (bubbletea program entry)

**Files:**
- Modify: `cmd/publish.go`

- [ ] **Step 1: Update import (line 22)**

```go
// FROM:
tea "github.com/charmbracelet/bubbletea"
// TO:
tea "charm.land/bubbletea/v2"
```

- [ ] **Step 2: Update NewProgram call (line 49)**

```go
// FROM:
p := tea.NewProgram(model, tea.WithAltScreen())
// TO:
p := tea.NewProgram(model)
```

The `AltScreen` is now controlled by the View in the model (already handled in Task 7).

- [ ] **Step 3: Commit**

```bash
git add cmd/publish.go
git commit -m "refactor: migrate cmd/publish.go to bubbletea v2"
```

---

### Task 10: Migrate cmd/subscribe.go, cmd/unsubscribe.go, cmd/init.go (huh forms)

**Files:**
- Modify: `cmd/subscribe.go`
- Modify: `cmd/unsubscribe.go`
- Modify: `cmd/init.go`

- [ ] **Step 1: Update imports in all three files**

cmd/subscribe.go (lines 25-26):
```go
// FROM:
"github.com/charmbracelet/huh"
"github.com/charmbracelet/lipgloss"
// TO:
"charm.land/huh/v2"
"charm.land/lipgloss/v2"
```

cmd/unsubscribe.go (line 10):
```go
// FROM:
"github.com/charmbracelet/huh"
// TO:
"charm.land/huh/v2"
```

cmd/init.go (line 23):
```go
// FROM:
"github.com/charmbracelet/huh"
// TO:
"charm.land/huh/v2"
```

- [ ] **Step 2: Verify huh form API compatibility**

All three files use the same huh patterns:
- `huh.NewForm()`, `huh.NewGroup()`, `huh.NewInput()`, `huh.NewConfirm()`, `huh.NewSelect[string]()`, `huh.NewMultiSelect[string]()`
- `huh.NewOption[string](label, value)` -- in v2 this may change to `huh.NewOption(label, value)` (type inference)
- `.Title()`, `.Value()`, `.Options()`, `.Validate()`, `.Description()` builder methods
- `form.Run()`

Fix any API changes. Pay special attention to generic type parameters -- v2 may remove the need for explicit `[string]` type arguments.

- [ ] **Step 3: Commit**

```bash
git add cmd/subscribe.go cmd/unsubscribe.go cmd/init.go
git commit -m "refactor: migrate huh forms to v2 in cmd/"
```

---

### Task 11: Migrate cmd/info.go and cmd/providers.go (glamour)

**Files:**
- Modify: `cmd/info.go`
- Modify: `cmd/providers.go`

- [ ] **Step 1: Update imports**

cmd/info.go (line 21):
```go
// FROM:
"github.com/charmbracelet/glamour"
// TO:
"charm.land/glamour/v2"
```

cmd/providers.go (line 21):
```go
// FROM:
"github.com/charmbracelet/glamour"
// TO:
"charm.land/glamour/v2"
```

- [ ] **Step 2: Verify glamour API compatibility**

Both files use:
```go
r, _ := glamour.NewTermRenderer(
    glamour.WithAutoStyle(),
    glamour.WithWordWrap(80),
)
out, _ := r.Render(markdownString)
```

Check that `glamour.NewTermRenderer()`, `glamour.WithAutoStyle()`, `glamour.WithWordWrap()`, and `r.Render()` still work in v2. Fix any changes.

- [ ] **Step 3: Commit**

```bash
git add cmd/info.go cmd/providers.go
git commit -m "refactor: migrate glamour to v2 in cmd/"
```

---

### Task 12: Clean up go.mod and final verification

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Run go mod tidy**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go mod tidy
```

This removes the old v1 charmbracelet dependencies and cleans up go.sum.

- [ ] **Step 2: Verify build**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
```

- [ ] **Step 3: Run tests**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./...
```

- [ ] **Step 4: Run linter**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run
```

Fix any issues.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: remove v1 charmbracelet dependencies, run go mod tidy"
```
