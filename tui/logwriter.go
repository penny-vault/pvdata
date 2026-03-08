package tui

import (
	"os"
	"sync"
)

// LogEntry represents a single log line for the TUI.
type LogEntry struct {
	Line string
}

// DualWriter writes to both a file and a channel for TUI consumption.
type DualWriter struct {
	file *os.File
	ch   chan LogEntry
	mu   sync.Mutex
}

// NewDualWriter creates a writer that outputs to the given file path and a channel.
// The channel is buffered to avoid blocking the logger.
func NewDualWriter(filePath string) (*DualWriter, error) {
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	return &DualWriter{
		file: f,
		ch:   make(chan LogEntry, 1000),
	}, nil
}

// Write implements io.Writer. It writes to both the file and the channel.
func (w *DualWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err = w.file.Write(p)
	if err != nil {
		return n, err
	}

	// Non-blocking send to channel
	select {
	case w.ch <- LogEntry{Line: string(p)}:
	default:
		// Drop the log entry if the channel is full
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
