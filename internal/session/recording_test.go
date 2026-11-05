package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordingWritesCastAndTranscript(t *testing.T) {
	var cast, transcript bytes.Buffer
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	rec, err := NewRecording(&cast, &transcript, 120, 40, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Write([]byte("hello \x1b[31mred\x1b[0m\n")); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(cast.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("cast lines = %d: %q", len(lines), cast.String())
	}
	var header castHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Version != 2 || header.Width != 120 || header.Height != 40 || header.Timestamp != start.Unix() {
		t.Fatalf("unexpected header: %+v", header)
	}

	var event []json.RawMessage
	if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
		t.Fatal(err)
	}
	if len(event) != 3 {
		t.Fatalf("event = %#v", event)
	}
	var kind, data string
	_ = json.Unmarshal(event[1], &kind)
	_ = json.Unmarshal(event[2], &data)
	if kind != "o" || !strings.Contains(data, "hello") {
		t.Fatalf("unexpected event: %q %q", kind, data)
	}
	if got := transcript.String(); got != "hello red\n" {
		t.Fatalf("transcript = %q", got)
	}
}

func TestNewFileRecording(t *testing.T) {
	dir := t.TempDir()
	rec, castPath, err := NewFileRecording(dir, "prod/db", 80, 24, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Write([]byte("file transcript\n")); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(castPath, ".cast") {
		t.Fatalf("cast path = %q", castPath)
	}
	castData, err := os.ReadFile(castPath)
	if err != nil {
		t.Fatal(err)
	}
	var header castHeaderWithEnv
	if err := json.Unmarshal(bytes.SplitN(castData, []byte("\n"), 2)[0], &header); err != nil {
		t.Fatal(err)
	}
	if header.Env["WHOP_HOST"] != "prod/db" {
		t.Fatalf("recorded host metadata = %#v", header.Env)
	}
	transcriptPath := strings.TrimSuffix(castPath, ".cast") + ".txt"
	for _, path := range []string{castPath, transcriptPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", path, info.Mode().Perm())
		}
	}
	transcript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(transcript) != "file transcript\n" {
		t.Fatalf("transcript = %q", transcript)
	}
}

func TestReplay(t *testing.T) {
	cast := `{"version":2,"width":80,"height":24,"timestamp":0}
[0.0,"o","hello"]
[0.1,"o"," world\n"]
`
	var output bytes.Buffer
	if err := Replay(strings.NewReader(cast), &output, 1000, func(time.Duration) {}); err != nil {
		t.Fatal(err)
	}
	plain := StripANSI(output.String())
	if !strings.Contains(plain, "hello") || !strings.Contains(plain, "world") {
		t.Fatalf("replay output missing text: %q", plain)
	}
}

func TestReplayRejectsUnsupportedVersion(t *testing.T) {
	cast := `{"version":1,"width":80,"height":24}`
	if err := Replay(strings.NewReader(cast), nil, 1, nil); err == nil {
		t.Fatal("expected version error")
	}
}

func TestSearchTranscripts(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "sub", "b.txt")
	if err := os.MkdirAll(filepath.Dir(fileB), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileA, []byte("first line\nPROD DB backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("staging\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	matches, err := Search(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != fileA || matches[0].Line != 2 {
		t.Fatalf("unexpected matches: %#v", matches)
	}

	missing, err := Search(filepath.Join(dir, "missing"), "prod")
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing dir: %#v %v", missing, err)
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("prod/db 01"); got != "prod_db_01" {
		t.Fatalf("sanitizeName = %q", got)
	}
	if got := sanitizeName("///"); got != "session" {
		t.Fatalf("sanitizeName = %q", got)
	}
}

func TestRecordingRedactsTranscriptByDefault(t *testing.T) {
	var cast, transcript bytes.Buffer
	rec, err := NewRecording(&cast, &transcript, 80, 24, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{
		"Password: hun",
		"ter2\nAPI_KEY=abc123\nAuthorization: Bearer bearer-secret\n",
		"token=tail-secret",
	} {
		if _, err := rec.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	plain := transcript.String()
	for _, secret := range []string{"hunter2", "abc123", "bearer-secret", "tail-secret"} {
		if strings.Contains(plain, secret) {
			t.Fatalf("transcript leaked %q: %q", secret, plain)
		}
	}
	if count := strings.Count(plain, "[REDACTED]"); count != 4 {
		t.Fatalf("redaction count = %d: %q", count, plain)
	}

	assertValidCastJSONL(t, cast.String())
	castText := castEventData(t, cast.String())
	if !strings.Contains(castText, "hunter2") {
		t.Fatalf("cast should preserve the original playback stream: %q", castText)
	}
}

func TestRecordingAppliesBackspaceToTranscript(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts RecordingOptions
	}{
		{name: "redacted"},
		{name: "unredacted", opts: RecordingOptions{DisableRedaction: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cast, transcript bytes.Buffer
			rec, err := NewRecordingWithOptions(&cast, &transcript, 80, 24, time.Now(), tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, chunk := range []string{
				"$ hello",
				"\b \b\b \b\b \b\b \b\b \b",
				"ls\n",
			} {
				if _, err := rec.Write([]byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			if err := rec.Close(); err != nil {
				t.Fatal(err)
			}
			if got := transcript.String(); got != "$ ls\n" {
				t.Fatalf("transcript = %q", got)
			}
			if raw := castEventData(t, cast.String()); !strings.Contains(raw, "hello\b") {
				t.Fatalf("cast should retain terminal erase sequences: %q", raw)
			}
		})
	}
}

func TestRecordingCustomRedactionRules(t *testing.T) {
	var transcript bytes.Buffer
	rec, err := NewRecordingWithOptions(nil, &transcript, 80, 24, time.Now(), RecordingOptions{
		RedactionRules: []RedactionRule{{
			Name:        "bearer",
			Pattern:     `(?i)bearer\s+\S+`,
			Replacement: "Bearer [REDACTED]",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Write([]byte("Bearer custom-secret\n")); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if got := transcript.String(); got != "Bearer [REDACTED]\n" {
		t.Fatalf("transcript = %q", got)
	}
}

func TestRecordingCanDisableRedaction(t *testing.T) {
	var transcript bytes.Buffer
	rec, err := NewRecordingWithOptions(nil, &transcript, 80, 24, time.Now(), RecordingOptions{
		DisableRedaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Write([]byte("Password: visible\n")); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if got := transcript.String(); got != "Password: visible\n" {
		t.Fatalf("transcript = %q", got)
	}
}

func TestRecordingRejectsInvalidRedactionPattern(t *testing.T) {
	_, err := NewRecordingWithOptions(nil, &bytes.Buffer{}, 80, 24, time.Now(), RecordingOptions{
		RedactionRules: []RedactionRule{{Pattern: "["}},
	})
	if err == nil {
		t.Fatal("expected invalid pattern error")
	}
}

func assertValidCastJSONL(t *testing.T, value string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("empty cast")
	}
	for index, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("cast line %d is not valid JSON: %q", index+1, line)
		}
	}
}

func castEventData(t *testing.T, value string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(value), "\n")
	var result strings.Builder
	for _, line := range lines[1:] {
		var event []json.RawMessage
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if len(event) != 3 {
			t.Fatalf("event = %#v", event)
		}
		var data string
		if err := json.Unmarshal(event[2], &data); err != nil {
			t.Fatal(err)
		}
		result.WriteString(data)
	}
	return result.String()
}

type castHeaderWithEnv struct {
	Env map[string]string `json:"env"`
}
