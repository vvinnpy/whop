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
