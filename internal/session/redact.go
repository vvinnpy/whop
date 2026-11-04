package session

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const maxTranscriptLineBuffer = 64 * 1024

// RedactionRule replaces matching text in the plain-text transcript.
// Replacement uses Go regexp replacement syntax, including $1 capture groups.
type RedactionRule struct {
	Name        string
	Pattern     string
	Replacement string
}

// RecordingOptions controls transcript redaction.
//
// Redaction applies only to the plain-text transcript. The asciicast stream is
// left byte-for-byte intact so terminal playback and control sequences remain
// valid. DisableRedaction writes the ANSI-stripped transcript without applying
// any rules. A nil RedactionRules value uses DefaultRedactionRules; an explicit
// empty slice disables rules while retaining the normal transcript writer.
type RecordingOptions struct {
	DisableRedaction bool
	RedactionRules   []RedactionRule
	// Host is written to the asciicast env header as TTYDECK_HOST so transcript
	// indexes can retain the SSH alias even after files are moved.
	Host string
}

// DefaultRedactionRules returns conservative rules for common credential output.
func DefaultRedactionRules() []RedactionRule {
	return []RedactionRule{
		{
			Name:        "assignment",
			Pattern:     `(?i)\b(password|passwd|passphrase|secret|token|api[_-]?key)(\s*[:=]\s*)(\S+)`,
			Replacement: `${1}${2}[REDACTED]`,
		},
		{
			Name:        "authorization-header",
			Pattern:     `(?i)\b(authorization\s*:\s*(?:bearer|basic)\s+)(\S+)`,
			Replacement: `${1}[REDACTED]`,
		},
	}
}

type compiledRedactionRule struct {
	pattern     *regexp.Regexp
	replacement string
}

type transcriptRedactor struct {
	dst     io.Writer
	rules   []compiledRedactionRule
	pending []byte
}

func newTranscriptRedactor(dst io.Writer, opts RecordingOptions) (*transcriptRedactor, error) {
	if opts.DisableRedaction {
		return nil, nil
	}
	rules := opts.RedactionRules
	if rules == nil {
		rules = DefaultRedactionRules()
	}
	compiled, err := compileRedactionRules(rules)
	if err != nil {
		return nil, err
	}
	if dst == nil || len(compiled) == 0 {
		return nil, nil
	}
	return &transcriptRedactor{dst: dst, rules: compiled}, nil
}

func compileRedactionRules(rules []RedactionRule) ([]compiledRedactionRule, error) {
	compiled := make([]compiledRedactionRule, 0, len(rules))
	for i, rule := range rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			name = fmt.Sprintf("rule-%d", i+1)
		}
		if strings.TrimSpace(rule.Pattern) == "" {
			return nil, fmt.Errorf("session: redaction rule %s has empty pattern", name)
		}
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("session: compile redaction rule %s: %w", name, err)
		}
		compiled = append(compiled, compiledRedactionRule{
			pattern:     pattern,
			replacement: rule.Replacement,
		})
	}
	return compiled, nil
}

func (r *transcriptRedactor) Write(data []byte) (int, error) {
	if r == nil {
		return len(data), nil
	}
	r.pending = append(r.pending, ansi.Strip(string(data))...)
	for {
		index := bytes.IndexByte(r.pending, '\n')
		if index < 0 {
			break
		}
		if err := r.writeSegment(r.pending[:index+1]); err != nil {
			return 0, err
		}
		r.pending = r.pending[index+1:]
	}
	if len(r.pending) >= maxTranscriptLineBuffer {
		if err := r.flush(); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

func (r *transcriptRedactor) Flush() error {
	if r == nil {
		return nil
	}
	return r.flush()
}

func (r *transcriptRedactor) flush() error {
	if len(r.pending) == 0 {
		return nil
	}
	segment := r.pending
	r.pending = nil
	return r.writeSegment(segment)
}

func (r *transcriptRedactor) writeSegment(segment []byte) error {
	text := string(segment)
	for _, rule := range r.rules {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	// Remove any control bytes that could accidentally be introduced by a
	// custom replacement while preserving normal whitespace.
	text = strings.Map(func(value rune) rune {
		if unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t' {
			return -1
		}
		return value
	}, text)
	written, err := io.WriteString(r.dst, text)
	if err != nil {
		return fmt.Errorf("session: write redacted transcript: %w", err)
	}
	if written != len(text) {
		return io.ErrShortWrite
	}
	return nil
}
