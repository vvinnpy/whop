package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeIndexCast(t *testing.T, path, header string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(header+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeIndexTranscript(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func v2Header(t *testing.T, timestamp int64, env map[string]string) string {
	t.Helper()
	value := map[string]any{
		"version":   2,
		"width":     120,
		"height":    40,
		"timestamp": timestamp,
	}
	if env != nil {
		value["env"] = env
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestIndexDirectoryNormal(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	base := filepath.Join(nested, "prod-db-20261103-100000.000")
	writeIndexCast(t, base+".cast", v2Header(t, started.Unix(), nil))
	writeIndexTranscript(t, base+".txt", "backup started\nbackup completed\n")

	// A cast without a same-name transcript must not produce a document.
	writeIndexCast(t, filepath.Join(dir, "orphan-20261103-090000.000.cast"), v2Header(t, started.Unix(), nil))

	documents, err := IndexDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("documents = %#v", documents)
	}
	document := documents[0]
	if !strings.HasSuffix(document.SessionID, ":nested/prod-db-20261103-100000.000") {
		t.Fatalf("session id = %q", document.SessionID)
	}
	if document.Host != "prod-db" {
		t.Fatalf("host = %q", document.Host)
	}
	if !document.StartedAt.Equal(started) {
		t.Fatalf("started at = %s, want %s", document.StartedAt, started)
	}
	if document.Text != "backup started\nbackup completed\n" {
		t.Fatalf("text = %q", document.Text)
	}
	wantPath := filepath.Join(nested, "prod-db-20261103-100000.000.txt")
	if document.Path != wantPath {
		t.Fatalf("path = %q, want %q", document.Path, wantPath)
	}
}

func TestIndexDirectoryUsesWHOPHost(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "filename-host-20261103-100000.000")
	writeIndexCast(t, base+".cast", v2Header(t, 1762164000, map[string]string{
		"TERM":         "xterm-256color",
		"WHOP_HOST": "db.example.com",
	}))
	writeIndexTranscript(t, base+".txt", "hello\n")

	documents, err := IndexDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("documents = %#v", documents)
	}
	if documents[0].Host != "db.example.com" {
		t.Fatalf("host = %q", documents[0].Host)
	}
}

func TestIndexDirectoryFallsBackToFilenameHost(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "prod-db-20261103-100000.000")
	writeIndexCast(t, base+".cast", v2Header(t, 1762164000, map[string]string{"TERM": "xterm-256color"}))
	writeIndexTranscript(t, base+".txt", "hello\n")

	documents, err := IndexDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].Host != "prod-db" {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestIndexDirectoryMissingTranscriptIsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeIndexCast(t, filepath.Join(dir, "prod-db-20261103-100000.000.cast"), v2Header(t, 1762164000, nil))

	documents, err := IndexDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 0 {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestIndexDirectoryLegacyAndNoEnv(t *testing.T) {
	t.Run("v1", func(t *testing.T) {
		dir := t.TempDir()
		base := filepath.Join(dir, "legacy-host-20261103-100000.500")
		legacy, err := json.Marshal(map[string]any{
			"version": 1,
			"width":   80,
			"height":  24,
			"stdout":  [][]any{{0.1, "old output\r\n"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		writeIndexCast(t, base+".cast", string(legacy))
		writeIndexTranscript(t, base+".txt", "old output\n")

		documents, err := IndexDirectory(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(documents) != 1 {
			t.Fatalf("documents = %#v", documents)
		}
		if documents[0].Host != "legacy-host" {
			t.Fatalf("host = %q", documents[0].Host)
		}
		wantStarted, ok := startedAtFromBase("legacy-host-20261103-100000.500")
		if !ok || !documents[0].StartedAt.Equal(wantStarted.UTC()) {
			t.Fatalf("started at = %s, want %s", documents[0].StartedAt, wantStarted.UTC())
		}
	})

	t.Run("v2 without env", func(t *testing.T) {
		dir := t.TempDir()
		base := filepath.Join(dir, "plain-host-20261103-100000.000")
		writeIndexCast(t, base+".cast", v2Header(t, 0, nil))
		writeIndexTranscript(t, base+".txt", "output\n")

		documents, err := IndexDirectory(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(documents) != 1 || documents[0].Host != "plain-host" {
			t.Fatalf("documents = %#v", documents)
		}
	})
}

func TestIndexDirectoryWithoutCastUsesFilenameFallbacks(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "castless-host-20261103-100000.250")
	writeIndexTranscript(t, base+".txt", "recorded without cast\n")

	documents, err := IndexDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("documents = %#v", documents)
	}
	if documents[0].Host != "castless-host" {
		t.Fatalf("host = %q", documents[0].Host)
	}
	wantStarted, _ := startedAtFromBase("castless-host-20261103-100000.250")
	if !documents[0].StartedAt.Equal(wantStarted.UTC()) {
		t.Fatalf("started at = %s, want %s", documents[0].StartedAt, wantStarted.UTC())
	}
}

func TestIndexDirectoryPreservesHTMLAndASCII(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "web-host-20261103-100000.000")
	writeIndexCast(t, base+".cast", v2Header(t, 1762164000, nil))
	text := "<b>bold</b>\n\tASCII text\n\x1b[31mred\x1b[0m\n"
	writeIndexTranscript(t, base+".txt", text)
	// HTML exports are not transcripts and must not be indexed.
	writeIndexTranscript(t, base+".html", "<html><body>export</body></html>")

	documents, err := IndexDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("documents = %#v", documents)
	}
	if documents[0].Text != text {
		t.Fatalf("text was modified: %q", documents[0].Text)
	}
}

func TestIndexDirectorySortsNewestFirstStably(t *testing.T) {
	dir := t.TempDir()
	files := []struct {
		base      string
		timestamp int64
	}{
		{base: "alpha-20261103-120000.000", timestamp: 1762164000},
		{base: "zeta-20261103-130000.000", timestamp: 1762167600},
		{base: "beta-20261103-130000.000", timestamp: 1762167600},
		{base: "older-20261103-110000.000", timestamp: 1762160400},
	}
	for _, file := range files {
		path := filepath.Join(dir, file.base)
		writeIndexCast(t, path+".cast", v2Header(t, file.timestamp, nil))
		writeIndexTranscript(t, path+".txt", file.base+"\n")
	}

	want := []string{
		"beta-20261103-130000.000",
		"zeta-20261103-130000.000",
		"alpha-20261103-120000.000",
		"older-20261103-110000.000",
	}
	for attempt := 0; attempt < 3; attempt++ {
		documents, err := IndexDirectory(dir)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, len(documents))
		for index := range documents {
			parts := strings.SplitN(documents[index].SessionID, ":", 2)
			got[index] = parts[len(parts)-1]
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("attempt %d: session order = %#v, want %#v", attempt, got, want)
		}
	}
}

func TestIndexDirectoryInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	brokenBase := filepath.Join(dir, "broken-20261103-100000.000")
	writeIndexCast(t, brokenBase+".cast", "{not json")
	writeIndexTranscript(t, brokenBase+".txt", "still readable\n")

	_, err := IndexDirectory(dir)
	if err == nil {
		t.Fatal("expected invalid cast error")
	}
	if !strings.Contains(err.Error(), brokenBase+".cast") {
		t.Fatalf("error does not identify cast path: %v", err)
	}

	documents, err := IndexDirectoryWithOptions(dir, IndexOptions{SkipInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 0 {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestIndexDirectoryIgnoresEmptyTranscripts(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "empty-20261103-100000.000")
	writeIndexCast(t, base+".cast", v2Header(t, 1762164000, nil))
	writeIndexTranscript(t, base+".txt", " \n\t\n")

	documents, err := IndexDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 0 {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestIndexDirectoryMissingDirectory(t *testing.T) {
	documents, err := IndexDirectory(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 0 {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestIndexDirectoryRejectsFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	writeIndexTranscript(t, path, "text\n")
	_, err := IndexDirectory(path)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestIndexCastMetadataErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.cast")
	tests := map[string]string{
		"empty":       "",
		"unsupported": `{"version":3}`,
		"array":       `[0,"o","text"]`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCastMetadata(path); err == nil {
				t.Fatal("expected metadata error")
			}
		})
	}
	if _, err := readCastMetadata(filepath.Join(dir, "missing.cast")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cast error = %v", err)
	}
}

func TestIndexDirectorySessionIDsAreRootScoped(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	for _, dir := range []string{firstDir, secondDir} {
		base := filepath.Join(dir, "same-20261103-100000.000")
		writeIndexCast(t, base+".cast", v2Header(t, 1762164000, nil))
		writeIndexTranscript(t, base+".txt", "same filename\n")
	}
	first, err := IndexDirectory(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := IndexDirectory(secondDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if first[0].SessionID == second[0].SessionID {
		t.Fatalf("session IDs collide across roots: %q", first[0].SessionID)
	}
}

func TestIndexDirectoryRejectsSymlinkOutsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	writeIndexTranscript(t, outsideFile, "outside secret\n")
	link := filepath.Join(root, "linked-20261103-100000.000.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}
	_, err := IndexDirectory(root)
	if err == nil || !strings.Contains(err.Error(), "escapes recording directory") {
		t.Fatalf("expected escape error, got %v", err)
	}
}

func TestIndexDirectoryFollowsRootSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	realRoot := t.TempDir()
	base := filepath.Join(realRoot, "symlink-root-20261103-100000.000")
	writeIndexCast(t, base+".cast", v2Header(t, 1762164000, nil))
	writeIndexTranscript(t, base+".txt", "through symlink\n")
	link := filepath.Join(t.TempDir(), "recordings")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	documents, err := IndexDirectory(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || !strings.Contains(documents[0].Path, "symlink-root-20261103-100000.000.txt") {
		t.Fatalf("documents = %#v", documents)
	}
}
