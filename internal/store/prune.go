package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
)

// ReconcileTranscriptRoot removes indexed transcripts beneath root that are no
// longer present in keepPaths. It is used after a directory scan so deleted or
// invalid recordings stop appearing in search results. Indexing the current
// files remains the caller's responsibility.
func (s *Store) ReconcileTranscriptRoot(root string, keepPaths []string) (int, error) {
	if root == "" {
		return 0, fmt.Errorf("store: empty transcript root")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, fmt.Errorf("store: resolve transcript root: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	absRoot = filepath.Clean(absRoot)

	keep := make(map[string]bool, len(keepPaths))
	for _, path := range keepPaths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return 0, fmt.Errorf("store: resolve transcript path %s: %w", path, err)
		}
		keep[filepath.Clean(absolute)] = true
	}

	rows, err := s.db.Query(`SELECT id, path FROM transcript_index`)
	if err != nil {
		return 0, fmt.Errorf("store: list indexed transcripts: %w", err)
	}
	type indexedPath struct {
		id   int64
		path string
	}
	var stale []indexedPath
	for rows.Next() {
		var item indexedPath
		if err := rows.Scan(&item.id, &item.path); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: scan indexed transcript: %w", err)
		}
		if !pathWithin(absRoot, item.path) || keep[filepath.Clean(item.path)] {
			continue
		}
		stale = append(stale, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("store: iterate indexed transcripts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("store: close indexed transcripts: %w", err)
	}
	if len(stale) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: begin transcript reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range stale {
		if err := deleteTranscriptRow(tx, item.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit transcript reconcile: %w", err)
	}
	return len(stale), nil
}

func pathWithin(root, path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, filepath.Clean(absolute))
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return len(relative) < 3 || relative[:3] != ".."+string(filepath.Separator)
}

func deleteTranscriptRow(tx *sql.Tx, id int64) error {
	if _, err := tx.Exec(`DELETE FROM transcript_fts_trigram WHERE rowid = ?`, id); err != nil {
		return fmt.Errorf("store: delete transcript fts row %d: %w", id, err)
	}
	if _, err := tx.Exec(`DELETE FROM transcript_index WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete transcript index row %d: %w", id, err)
	}
	return nil
}
