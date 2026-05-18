package tui

import (
	"io"
	"os"
	"sync"

	"github.com/rs/zerolog"
)

const maxLogFileSize = 10 * 1024 * 1024 // 10 MB

// LogEntry represents a single log line for the TUI.
type LogEntry struct {
	Line string
}

// DualWriter receives raw JSON from zerolog, writes JSON to a file, and
// writes console-formatted output to either a TUI channel (when the
// dashboard is active) or stderr (when running headless). Without the
// stderr fallback a headless `pvdata run` produces no visible output:
// the channel fills, drops every subsequent line, and the operator sees
// nothing while a multi-hour walk is in progress.
type DualWriter struct {
	file       *os.File
	console    zerolog.ConsoleWriter
	stderrCons zerolog.ConsoleWriter
	ch         chan LogEntry
	hasTUI     bool
	mu         sync.Mutex
}

// NewDualWriter creates a writer that outputs JSON to the given file
// path and console-formatted text to a channel (for TUI consumption)
// or stderr (when hasTUI is false). If the file exceeds 10 MB it is
// truncated before writing. hasTUI is set to true only by the TUI
// startup path; every other run gets the stderr console.
func NewDualWriter(filePath string, hasTUI bool) (*DualWriter, error) {
	// Truncate if file exceeds size limit
	if info, err := os.Stat(filePath); err == nil && info.Size() > maxLogFileSize {
		if err := os.Truncate(filePath, 0); err != nil {
			return nil, err
		}
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	ch := make(chan LogEntry, 1000)

	// Console writer formats JSON into human-readable colored output
	// for the TUI channel.
	chanW := &chanWriter{ch: ch}
	consoleW := zerolog.ConsoleWriter{Out: chanW}

	// stderr console writer is wired regardless of hasTUI so an operator
	// who switches to/from TUI mid-run still gets writes via the right
	// path. The Write method picks which one to forward to based on
	// hasTUI; the unused one stays idle.
	stderrW := zerolog.ConsoleWriter{Out: io.Writer(os.Stderr)}

	return &DualWriter{
		file:       f,
		console:    consoleW,
		stderrCons: stderrW,
		ch:         ch,
		hasTUI:     hasTUI,
	}, nil
}

// Write receives raw JSON from zerolog. It always writes JSON to the
// file, then forwards a console-formatted copy to either the TUI
// channel or stderr depending on hasTUI.
func (w *DualWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Write raw JSON to file
	n, err = w.file.Write(p)
	if err != nil {
		return n, err
	}

	if w.hasTUI {
		_, _ = w.console.Write(p)
	} else {
		_, _ = w.stderrCons.Write(p)
	}

	return n, nil
}

// LogChan returns the channel that TUI can read log entries from.
func (w *DualWriter) LogChan() <-chan LogEntry {
	return w.ch
}

// Close closes the file and channel.
func (w *DualWriter) Close() error {
	close(w.ch)
	return w.file.Close()
}

// chanWriter is an io.Writer that sends output to a channel.
type chanWriter struct {
	ch chan LogEntry
}

func (w *chanWriter) Write(p []byte) (n int, err error) {
	select {
	case w.ch <- LogEntry{Line: string(p)}:
	default:
		// Drop the log entry if the channel is full
	}

	return len(p), nil
}
