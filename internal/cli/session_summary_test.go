package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestLastSessionOutputLine(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "strips ansi and keeps last non-empty line",
			output: "\x1b[32mWelcome to GitLab, @user!\x1b[0m\n\n",
			want:   "Welcome to GitLab, @user!",
		},
		{
			name:   "handles carriage returns",
			output: "progress 10%\rprogress 100%\r",
			want:   "progress 100%",
		},
		{
			name:   "drops control characters",
			output: "warning:\x00 disk almost full\n",
			want:   "warning: disk almost full",
		},
		{
			name:   "empty output",
			output: "\n\r\x1b[0m",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastSessionOutputLine(tt.output); got != tt.want {
				t.Fatalf("lastSessionOutputLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionOutputCaptureForwardsAndKeepsTail(t *testing.T) {
	var output bytes.Buffer
	capture := newSessionOutputCapture(&output)
	input := "first line\nWelcome to GitLab, @user!\n"
	if _, err := io.WriteString(capture, input); err != nil {
		t.Fatal(err)
	}
	if output.String() != input {
		t.Fatalf("forwarded output = %q, want %q", output.String(), input)
	}
	if got := capture.LastLine(); got != "Welcome to GitLab, @user!" {
		t.Fatalf("LastLine() = %q", got)
	}
}

func TestAppendSessionSummary(t *testing.T) {
	withStdout := func(text string) *sessionOutputCapture {
		capture := newSessionOutputCapture(io.Discard)
		_, _ = io.WriteString(capture, text)
		return capture
	}
	tests := []struct {
		name       string
		inputBytes int64
		stdout     *sessionOutputCapture
		stderr     *sessionOutputCapture
		want       string
	}{
		{
			name:   "shows output when there was no input",
			stdout: withStdout("Welcome to GitLab, @user!\n"),
			want:   "session ended · gitlab · Welcome to GitLab, @user!",
		},
		{
			name:       "keeps notice clean after user input",
			inputBytes: 5,
			stdout:     withStdout("Welcome to GitLab, @user!\n"),
			want:       "session ended · gitlab",
		},
		{
			name:   "falls back to stderr",
			stdout: withStdout(""),
			stderr: withStdout("remote warning\n"),
			want:   "session ended · gitlab · remote warning",
		},
		{
			name:   "no output",
			stdout: withStdout(""),
			stderr: withStdout(""),
			want:   "session ended · gitlab",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appendSessionSummary("session ended · gitlab", tt.inputBytes, tt.stdout, tt.stderr); got != tt.want {
				t.Fatalf("appendSessionSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionInputCounterCountsAndUnwraps(t *testing.T) {
	for _, input := range []string{"exit\n", "\x03"} {
		reader := strings.NewReader(input)
		counter := newSessionInputCounter(reader)
		if counter.UnwrapReader() != reader {
			t.Fatal("UnwrapReader did not return the original reader")
		}
		_, _ = io.Copy(io.Discard, counter)
		if got := counter.Bytes(); got != int64(len(input)) {
			t.Fatalf("input %q bytes = %d, want %d", input, got, len(input))
		}
	}
}
