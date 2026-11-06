package cli

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const (
	sessionOutputTailLimit = 4096
	sessionSummaryMaxWidth = 160
)

// sessionInputCounter counts bytes actually read from the local terminal and
// forwarded to the remote session. It exposes the underlying reader so the SSH
// terminal layer can still discover and configure the real TTY.
type sessionInputCounter struct {
	reader io.Reader
	bytes  atomic.Int64
}

func newSessionInputCounter(reader io.Reader) *sessionInputCounter {
	return &sessionInputCounter{reader: reader}
}

func (r *sessionInputCounter) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes.Add(int64(n))
	return n, err
}

// UnwrapReader lets sshconn's terminal setup reach the underlying *os.File
// without bypassing this counter for actual reads.
func (r *sessionInputCounter) UnwrapReader() io.Reader {
	return r.reader
}

func (r *sessionInputCounter) Bytes() int64 {
	return r.bytes.Load()
}

// sessionOutputCapture forwards output to the real terminal while retaining a
// bounded tail for the post-session status line.
type sessionOutputCapture struct {
	mu     sync.Mutex
	target io.Writer
	tail   []byte
}

func newSessionOutputCapture(target io.Writer) *sessionOutputCapture {
	if target == nil {
		target = io.Discard
	}
	return &sessionOutputCapture{target: target}
}

func (w *sessionOutputCapture) Write(data []byte) (int, error) {
	n, err := w.target.Write(data)
	if n > 0 {
		w.mu.Lock()
		w.tail = append(w.tail, data[:n]...)
		if len(w.tail) > sessionOutputTailLimit {
			w.tail = w.tail[len(w.tail)-sessionOutputTailLimit:]
		}
		w.mu.Unlock()
	}
	return n, err
}

func (w *sessionOutputCapture) LastLine() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	tail := append([]byte(nil), w.tail...)
	w.mu.Unlock()
	return lastSessionOutputLine(string(tail))
}

func lastSessionOutputLine(output string) string {
	output = ansi.Strip(output)
	output = strings.ToValidUTF8(output, "")
	lines := strings.FieldsFunc(output, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, lines[i])
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return ansi.Truncate(line, sessionSummaryMaxWidth, "…")
	}
	return ""
}

func appendSessionSummary(text string, inputBytes int64, stdout, stderr *sessionOutputCapture) string {
	if inputBytes != 0 || text == "" {
		return text
	}
	summary := stdout.LastLine()
	if summary == "" {
		summary = stderr.LastLine()
	}
	if summary == "" {
		return text
	}
	return text + " · " + summary
}
