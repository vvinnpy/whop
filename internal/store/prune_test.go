package store

import (
	"path/filepath"
	"testing"
	"time"
)

func resolvedPruneTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReconcileTranscriptRootRemovesStaleRecords(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := resolvedPruneTempDir(t)
	kept := filepath.Join(root, "kept.txt")
	stale := filepath.Join(root, "nested", "stale.txt")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	for _, transcript := range []Transcript{
		{SessionID: "kept", Path: kept, Host: "kept", StartedAt: time.Now(), Text: "kept database"},
		{SessionID: "stale", Path: stale, Host: "stale", StartedAt: time.Now(), Text: "stale database"},
		{SessionID: "outside", Path: outside, Host: "outside", StartedAt: time.Now(), Text: "outside database"},
	} {
		if _, err := db.IndexTranscript(transcript); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := db.ReconcileTranscriptRoot(root, []string{kept})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	matches, err := db.SearchTranscripts("database")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %#v", matches)
	}
	for _, match := range matches {
		if match.Path == stale {
			t.Fatalf("stale transcript survived: %#v", matches)
		}
	}
}

func TestPathWithin(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	tests := []struct {
		path string
		want bool
	}{
		{path: filepath.Join(root, "a.txt"), want: true},
		{path: filepath.Join(root, "nested", "a.txt"), want: true},
		{path: filepath.Join(root, "..", "outside.txt"), want: false},
		{path: root + "sibling.txt", want: false},
	}
	for _, test := range tests {
		if got := pathWithin(root, test.path); got != test.want {
			t.Fatalf("pathWithin(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestSearchTranscriptsSupportsCJKSubstringsAndShortQueries(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.IndexTranscript(Transcript{
		SessionID: "cjk",
		Path:      "/recordings/cjk.txt",
		Host:      "prod-db",
		StartedAt: time.Now(),
		Text:      "生产数据库备份完成\n",
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"数据库", "数据", "数"} {
		matches, err := db.SearchTranscripts(query)
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if len(matches) != 1 || matches[0].Host != "prod-db" {
			t.Fatalf("query %q matches = %#v", query, matches)
		}
	}
}
