package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

type castHeader struct {
	Version   int   `json:"version"`
	Width     int   `json:"width"`
	Height    int   `json:"height"`
	Timestamp int64 `json:"timestamp"`
}

// Replay renders an asciicast v2 stream. speed <= 0 means real time.
func Replay(reader io.Reader, out io.Writer, speed float64, sleep func(time.Duration)) error {
	if speed <= 0 {
		speed = 1
	}
	if sleep == nil {
		sleep = time.Sleep
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("session: read cast header: %w", err)
		}
		return fmt.Errorf("session: empty cast")
	}
	var header castHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return fmt.Errorf("session: decode cast header: %w", err)
	}
	if header.Version != 2 {
		return fmt.Errorf("session: unsupported asciicast version %d", header.Version)
	}
	if header.Width <= 0 {
		header.Width = 80
	}
	if header.Height <= 0 {
		header.Height = 24
	}

	emulator := vt.NewEmulator(header.Width, header.Height)
	last := 0.0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return fmt.Errorf("session: decode cast event: %w", err)
		}
		if len(raw) < 3 {
			continue
		}
		var timestamp float64
		var kind, data string
		if err := json.Unmarshal(raw[0], &timestamp); err != nil {
			return fmt.Errorf("session: decode event timestamp: %w", err)
		}
		if err := json.Unmarshal(raw[1], &kind); err != nil {
			return fmt.Errorf("session: decode event kind: %w", err)
		}
		if err := json.Unmarshal(raw[2], &data); err != nil {
			return fmt.Errorf("session: decode event data: %w", err)
		}
		if timestamp > last {
			delay := time.Duration((timestamp - last) / speed * float64(time.Second))
			if delay > 0 {
				sleep(delay)
			}
			last = timestamp
		}
		if kind != "o" {
			continue
		}
		if _, err := emulator.Write([]byte(data)); err != nil {
			return fmt.Errorf("session: replay write: %w", err)
		}
		if out != nil {
			if _, err := io.WriteString(out, "\x1b[H\x1b[2J"+emulator.Render()); err != nil {
				return fmt.Errorf("session: replay output: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("session: read cast: %w", err)
	}
	if out != nil {
		if _, err := io.WriteString(out, "\x1b[H\x1b[2J"+emulator.Render()); err != nil {
			return fmt.Errorf("session: final replay output: %w", err)
		}
	}
	return nil
}

// ReplayFile replays a .cast file.
func ReplayFile(path string, out io.Writer, speed float64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("session: open cast %s: %w", path, err)
	}
	defer file.Close()
	return Replay(file, out, speed, nil)
}

// StripANSI removes terminal escape sequences from text.
func StripANSI(value string) string {
	return ansi.Strip(value)
}
