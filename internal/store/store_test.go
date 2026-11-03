package store

import (
	"path/filepath"
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
