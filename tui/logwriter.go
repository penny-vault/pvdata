package tui

import (
	"os"
	"sync"

	"github.com/rs/zerolog"
)

const maxLogFileSize = 10 * 1024 * 1024 // 10 MB

// LogEntry represents a single log line for the TUI.
type LogEntry struct {
	Line string
}

// DualWriter receives raw JSON from zerolog, writes JSON to a file,
// and writes console-formatted output to a channel for TUI consumption.
type DualWriter struct {
	file    *os.File
	console zerolog.ConsoleWriter
	ch      chan LogEntry
	mu      sync.Mutex
}

// NewDualWriter creates a writer that outputs JSON to the given file path
// and console-formatted text to a channel. If the file exceeds 10 MB it
// is truncated before writing.
func NewDualWriter(filePath string) (*DualWriter, error) {
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

	return &DualWriter{
		file:    f,
		console: consoleW,
		ch:      ch,
	}, nil
}

// Write receives raw JSON from zerolog. It writes JSON to the file
// and passes the JSON through a ConsoleWriter for the TUI channel.
func (w *DualWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Write raw JSON to file
	n, err = w.file.Write(p)
	if err != nil {
		return n, err
	}

	// Write console-formatted output to the TUI channel
	_, _ = w.console.Write(p)

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
