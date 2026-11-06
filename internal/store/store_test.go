package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	missing, err := db.Get("prod-db")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Favorite || missing.Notes != "" || missing.ConnectCount != 0 {
		t.Fatalf("unexpected missing metadata: %+v", missing)
	}

	if err := db.SetFavorite("prod-db", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNotes("prod-db", "primary database"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTags("prod-db", []string{"prod", "db", "prod"}); err != nil {
		t.Fatal(err)
	}
	service := false
	if err := db.SetLoginOverride("prod-db", &service); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRecord("prod-db", true); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	if err := db.RecordConnect("prod-db", at); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordConnect("prod-db", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	meta, err := db.Get("prod-db")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Favorite || meta.Notes != "primary database" || meta.ConnectCount != 2 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if meta.LoginOverride == nil || *meta.LoginOverride {
		t.Fatalf("login override not stored: %+v", meta)
	}
	if !meta.Record {
		t.Fatalf("record policy not stored: %+v", meta)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "db" || meta.Tags[1] != "prod" {
		t.Fatalf("unexpected tags: %#v", meta.Tags)
	}
	if !meta.LastConnectedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("last connected = %v", meta.LastConnectedAt)
	}

	all, err := db.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all["prod-db"].ConnectCount != 2 {
		t.Fatalf("unexpected all metadata: %#v", all)
	}

	if err := db.SetTags("prod-db", []string{"staging"}); err != nil {
		t.Fatal(err)
	}
	meta, _ = db.Get("prod-db")
	if len(meta.Tags) != 1 || meta.Tags[0] != "staging" {
		t.Fatalf("tags were not replaced: %#v", meta.Tags)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	meta, err = reopened.Get("prod-db")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Favorite || meta.ConnectCount != 2 || meta.Notes != "primary database" {
		t.Fatalf("metadata did not persist: %+v", meta)
	}
	if meta.LoginOverride == nil || *meta.LoginOverride {
		t.Fatalf("login override did not persist: %+v", meta)
	}
	if !meta.Record {
		t.Fatalf("record policy did not persist: %+v", meta)
	}
}

func TestSetLoginOverrideAutoClearsValue(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	service := false
	if err := db.SetLoginOverride("prod", &service); err != nil {
		t.Fatal(err)
	}
	if err := db.SetLoginOverride("prod", nil); err != nil {
		t.Fatal(err)
	}
	meta, err := db.Get("prod")
	if err != nil {
		t.Fatal(err)
	}
	if meta.LoginOverride != nil {
		t.Fatalf("auto override should be nil: %+v", meta)
	}
}

func TestOpenMigratesLegacyHostMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE host_meta (
		alias TEXT PRIMARY KEY,
		favorite INTEGER NOT NULL DEFAULT 0,
		notes TEXT NOT NULL DEFAULT '',
		last_connected_at TEXT,
		connect_count INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO host_meta(alias, favorite, notes) VALUES('prod', 1, 'legacy')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	meta, err := db.Get("prod")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Favorite || meta.Notes != "legacy" || meta.LoginOverride != nil || meta.Record {
		t.Fatalf("legacy metadata not preserved: %+v", meta)
	}
	login := true
	if err := db.SetLoginOverride("prod", &login); err != nil {
		t.Fatalf("migrated store rejected login override: %v", err)
	}
}

func TestStoreValidation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Get(" "); err == nil {
		t.Fatal("expected empty alias error")
	}
	if err := db.SetFavorite("", true); err == nil {
		t.Fatal("expected empty alias error")
	}
	if err := db.SetLoginOverride("", nil); err == nil {
		t.Fatal("expected empty alias error")
	}
	if err := db.SetRecord("", true); err == nil {
		t.Fatal("expected empty alias error")
	}
	if err := db.SetNotes("", "note"); err == nil {
		t.Fatal("expected empty alias error")
	}
	if err := db.SetTags("", []string{"tag"}); err == nil {
		t.Fatal("expected empty alias error")
	}
	if err := db.RecordConnect("", time.Now()); err == nil {
		t.Fatal("expected empty alias error")
	}
}

func TestSortTags(t *testing.T) {
	got := SortTags([]string{" prod ", "db", "", "prod", "db"})
	if len(got) != 2 || got[0] != "db" || got[1] != "prod" {
		t.Fatalf("unexpected tags: %#v", got)
	}
}

func TestSnippets(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.SetSnippet("disk", "df -h"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSnippet("uptime", "uptime"); err != nil {
		t.Fatal(err)
	}
	snippets, err := db.Snippets()
	if err != nil {
		t.Fatal(err)
	}
	if len(snippets) != 2 || snippets["disk"] != "df -h" || snippets["uptime"] != "uptime" {
		t.Fatalf("unexpected snippets: %#v", snippets)
	}

	if err := db.SetSnippet("disk", "df -ih"); err != nil {
		t.Fatal(err)
	}
	snippets, _ = db.Snippets()
	if snippets["disk"] != "df -ih" {
		t.Fatalf("snippet not updated: %#v", snippets)
	}

	if err := db.DeleteSnippet("disk"); err != nil {
		t.Fatal(err)
	}
	snippets, _ = db.Snippets()
	if _, ok := snippets["disk"]; ok {
		t.Fatalf("snippet not deleted: %#v", snippets)
	}
	if err := db.SetSnippet("", "x"); err == nil {
		t.Fatal("expected empty name error")
	}
	if err := db.SetSnippet("x", ""); err == nil {
		t.Fatal("expected empty command error")
	}
}

func TestTranscriptIndexSearchAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 11, 3, 10, 0, 0, 123456789, time.UTC)
	if err := db.IndexTranscript(Transcript{
		SessionID: "prod-db-1",
		Path:      "/recordings/prod-db-1.txt",
		Host:      "prod-db",
		StartedAt: started,
		Text:      "backup started\nproduction database backup completed\n",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.IndexTranscript(Transcript{
		SessionID: "staging-1",
		Path:      "/recordings/staging-1.txt",
		Host:      "staging",
		StartedAt: started.Add(time.Minute),
		Text:      "staging database maintenance\n",
	}); err != nil {
		t.Fatal(err)
	}

	matches, err := db.SearchTranscripts("database")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %#v", matches)
	}
	if matches[0].Host != "staging" && matches[1].Host != "staging" {
		t.Fatalf("missing staging result: %#v", matches)
	}
	var prod *TranscriptMatch
	for index := range matches {
		if matches[index].Host == "prod-db" {
			prod = &matches[index]
		}
	}
	if prod == nil {
		t.Fatalf("missing prod-db result: %#v", matches)
	}
	if prod.Path != "/recordings/prod-db-1.txt" || !prod.StartedAt.Equal(started) {
		t.Fatalf("unexpected prod result: %+v", *prod)
	}
	if !strings.Contains(strings.ToLower(prod.Snippet), "database") ||
		!strings.Contains(prod.Snippet, ">>>") || !strings.Contains(prod.Snippet, "<<<") {
		t.Fatalf("unexpected snippet: %q", prod.Snippet)
	}

	if err := db.IndexTranscript(Transcript{
		SessionID: "prod-db-1",
		Path:      "/recordings/prod-db-1.txt",
		Host:      "prod-db",
		StartedAt: started,
		Text:      "restore completed successfully\n",
	}); err != nil {
		t.Fatal(err)
	}
	oldMatches, err := db.SearchTranscripts("backup")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldMatches) != 0 {
		t.Fatalf("old index content survived replacement: %#v", oldMatches)
	}
	newMatches, err := db.SearchTranscripts("restore")
	if err != nil {
		t.Fatal(err)
	}
	if len(newMatches) != 1 || newMatches[0].Host != "prod-db" {
		t.Fatalf("replacement not searchable: %#v", newMatches)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.SearchTranscripts("restore")
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].Path != "/recordings/prod-db-1.txt" ||
		!persisted[0].StartedAt.Equal(started) {
		t.Fatalf("transcript index did not persist: %#v", persisted)
	}
}

func TestTranscriptIndexValidation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	valid := Transcript{
		SessionID: "session",
		Path:      "session.txt",
		Host:      "host",
		StartedAt: time.Now(),
		Text:      "text",
	}
	tests := []struct {
		name   string
		mutate func(*Transcript)
	}{
		{name: "session", mutate: func(value *Transcript) { value.SessionID = " " }},
		{name: "path", mutate: func(value *Transcript) { value.Path = "" }},
		{name: "host", mutate: func(value *Transcript) { value.Host = "" }},
		{name: "time", mutate: func(value *Transcript) { value.StartedAt = time.Time{} }},
		{name: "text", mutate: func(value *Transcript) { value.Text = " " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transcript := valid
			test.mutate(&transcript)
			if err := db.IndexTranscript(transcript); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestTranscriptQueryValidationAndEscaping(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.IndexTranscript(Transcript{
		SessionID: "session",
		Path:      "session.txt",
		Host:      "host",
		StartedAt: time.Now(),
		Text:      "disk full; disk:error; quoted \"full\" value\n",
	}); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"", "   ", "\n\t", "***", "\x00disk", strings.Repeat("x", maxTranscriptQueryLength+1)} {
		if _, err := db.SearchTranscripts(query); err == nil {
			t.Fatalf("query %q should fail", query)
		}
	}
	if _, err := db.SearchTranscripts(""); !errors.Is(err, ErrEmptyTranscriptQuery) {
		t.Fatalf("empty query error = %v", err)
	}
	if _, err := db.SearchTranscripts("***"); !errors.Is(err, ErrInvalidTranscriptQuery) {
		t.Fatalf("invalid query error = %v", err)
	}

	for _, query := range []string{`disk AND full`, `disk:*`, `quoted "full"`} {
		if _, err := db.SearchTranscripts(query); err != nil {
			t.Fatalf("literal query %q failed: %v", query, err)
		}
	}
}

func TestRenameHostMovesMetadataAndTags(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetFavorite("old", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNotes("old", "keep me"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTags("old", []string{"prod", "db"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RenameHost("old", "new"); err != nil {
		t.Fatal(err)
	}
	old, _ := db.Get("old")
	if old.Favorite || old.Notes != "" || len(old.Tags) != 0 {
		t.Fatalf("old metadata remains: %+v", old)
	}
	newMeta, err := db.Get("new")
	if err != nil {
		t.Fatal(err)
	}
	if !newMeta.Favorite || newMeta.Notes != "keep me" || len(newMeta.Tags) != 2 {
		t.Fatalf("new metadata = %+v", newMeta)
	}
	if err := db.SetFavorite("other", true); err != nil {
		t.Fatal(err)
	}
	if err := db.RenameHost("new", "other"); err == nil {
		t.Fatal("expected target metadata conflict")
	}
}

func TestAllIncludesTagsForMultipleHosts(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.SetFavorite("prod-db", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTags("prod-db", []string{"prod", "db"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNotes("staging", "test box"); err != nil {
		t.Fatal(err)
	}

	all, err := db.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all metadata = %#v", all)
	}
	if !all["prod-db"].Favorite || len(all["prod-db"].Tags) != 2 ||
		all["prod-db"].Tags[0] != "db" || all["prod-db"].Tags[1] != "prod" {
		t.Fatalf("prod-db metadata = %#v", all["prod-db"])
	}
	if all["staging"].Notes != "test box" || len(all["staging"].Tags) != 0 {
		t.Fatalf("staging metadata = %#v", all["staging"])
	}
}
