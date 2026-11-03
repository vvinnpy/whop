// Package session records and replays terminal sessions.
package session

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Recording writes asciicast v2 output events and a plain-text transcript.
type Recording struct {
	mu         sync.Mutex
	cast       io.Writer
	transcript io.Writer
	closers    []io.Closer
	start      time.Time
	closeOnce  sync.Once
	closeErr   error
}

// NewRecording creates a recording writer. cast and transcript may be nil.
func NewRecording(cast, transcript io.Writer, width, height int, start time.Time) (*Recording, error) {
	if start.IsZero() {
		start = time.Now()
	}
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	header, err := json.Marshal(map[string]any{
		"version":   2,
		"width":     width,
		"height":    height,
		"timestamp": start.Unix(),
		"env": map[string]string{
			"TERM": "xterm-256color",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("session: encode recording header: %w", err)
	}
	if cast != nil {
		if _, err := cast.Write(append(header, '\n')); err != nil {
			return nil, fmt.Errorf("session: write recording header: %w", err)
		}
	}
	return &Recording{cast: cast, transcript: transcript, start: start}, nil
}

// Write records one output chunk.
func (r *Recording) Write(data []byte) (int, error) {
	if r == nil {
		return len(data), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	elapsed := time.Since(r.start).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	if r.cast != nil {
		event, err := json.Marshal([]any{elapsed, "o", string(data)})
		if err != nil {
			return 0, fmt.Errorf("session: encode recording event: %w", err)
		}
		if _, err := r.cast.Write(append(event, '\n')); err != nil {
			return 0, fmt.Errorf("session: write recording event: %w", err)
		}
	}
	if r.transcript != nil {
		if _, err := io.WriteString(r.transcript, ansi.Strip(string(data))); err != nil {
			return 0, fmt.Errorf("session: write transcript: %w", err)
		}
	}
	return len(data), nil
}

// Close closes any files owned by the recording.
func (r *Recording) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		for _, closer := range r.closers {
			if err := closer.Close(); err != nil && r.closeErr == nil {
				r.closeErr = err
			}
		}
	})
	return r.closeErr
}

// NewFileRecording creates .cast and .txt files under dir.
func NewFileRecording(dir, alias string, width, height int, start time.Time) (*Recording, string, error) {
	if start.IsZero() {
		start = time.Now()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("session: create recording directory: %w", err)
	}
	base := sanitizeName(alias) + "-" + start.Format("20060102-150405.000")
	castPath := filepath.Join(dir, base+".cast")
	transcriptPath := filepath.Join(dir, base+".txt")

	castFile, err := os.OpenFile(castPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("session: create cast: %w", err)
	}
	transcriptFile, err := os.OpenFile(transcriptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = castFile.Close()
		_ = os.Remove(castPath)
		return nil, "", fmt.Errorf("session: create transcript: %w", err)
	}

	recording, err := NewRecording(castFile, transcriptFile, width, height, start)
	if err != nil {
		_ = castFile.Close()
		_ = transcriptFile.Close()
		_ = os.Remove(castPath)
		_ = os.Remove(transcriptPath)
		return nil, "", err
	}
	recording.closers = []io.Closer{transcriptFile, castFile}
	return recording, castPath, nil
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "session"
	}
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	name := strings.Trim(builder.String(), "._-")
	if name == "" {
		return "session"
	}
	return name
}
