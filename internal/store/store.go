// Package store persists whop metadata in SQLite.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Meta is user-owned metadata for one SSH alias.
type Meta struct {
	Favorite        bool
	Notes           string
	LastConnectedAt time.Time
	ConnectCount    int
	Tags            []string
	LoginOverride   *bool
	Record          bool
}

// Store is a SQLite-backed metadata store.
type Store struct {
	db *sql.DB
}

// Open opens or creates the metadata database.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("store: create data directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS host_meta (
			alias TEXT PRIMARY KEY,
			favorite INTEGER NOT NULL DEFAULT 0,
			notes TEXT NOT NULL DEFAULT '',
			last_connected_at TEXT,
			connect_count INTEGER NOT NULL DEFAULT 0,
			login_override INTEGER,
			record INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS host_tags (
			alias TEXT NOT NULL,
			tag TEXT NOT NULL,
			PRIMARY KEY (alias, tag)
		)`,
		`CREATE TABLE IF NOT EXISTS snippets (
			name TEXT PRIMARY KEY,
			command TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transcript_index (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL UNIQUE,
			path TEXT NOT NULL,
			host TEXT NOT NULL,
			started_at TEXT NOT NULL,
			file_size INTEGER NOT NULL DEFAULT -1,
			file_mtime INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS transcript_index_started_at
			ON transcript_index(started_at DESC)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS transcript_fts_trigram USING fts5(
			text,
			tokenize = 'trigram'
		)`,
		auditEventsTableDDL,
		`CREATE INDEX IF NOT EXISTS audit_events_at ON audit_events(at DESC, id DESC)`,
		macroTableDDL,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("store: init: %w", err)
		}
	}
	if err := s.ensureHostMetaColumns(); err != nil {
		return err
	}
	if err := s.ensureTranscriptColumns(); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureHostMetaColumns() error {
	columns, err := s.tableColumns("host_meta")
	if err != nil {
		return fmt.Errorf("store: inspect host_meta: %w", err)
	}
	if !columns["login_override"] {
		if _, err := s.db.Exec(`ALTER TABLE host_meta ADD COLUMN login_override INTEGER`); err != nil {
			return fmt.Errorf("store: add login_override: %w", err)
		}
	}
	if !columns["record"] {
		if _, err := s.db.Exec(`ALTER TABLE host_meta ADD COLUMN record INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("store: add record: %w", err)
		}
	}
	return nil
}

func (s *Store) ensureTranscriptColumns() error {
	columns, err := s.tableColumns("transcript_index")
	if err != nil {
		return fmt.Errorf("store: inspect transcript_index: %w", err)
	}
	if !columns["file_size"] {
		if _, err := s.db.Exec(`ALTER TABLE transcript_index ADD COLUMN file_size INTEGER NOT NULL DEFAULT -1`); err != nil {
			return fmt.Errorf("store: add file_size: %w", err)
		}
	}
	if !columns["file_mtime"] {
		if _, err := s.db.Exec(`ALTER TABLE transcript_index ADD COLUMN file_mtime INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("store: add file_mtime: %w", err)
		}
	}
	return nil
}

func (s *Store) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name      string
			columnTyp string
			notNull   int
			defaultV  sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &columnTyp, &notNull, &defaultV, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns metadata for alias. A missing row returns zero metadata.
func (s *Store) Get(alias string) (Meta, error) {
	if strings.TrimSpace(alias) == "" {
		return Meta{}, errors.New("store: empty alias")
	}
	var (
		favorite      int
		notes         string
		last          sql.NullString
		count         int
		loginOverride sql.NullInt64
		record        int
	)
	err := s.db.QueryRow(
		`SELECT favorite, notes, last_connected_at, connect_count, login_override, record FROM host_meta WHERE alias = ?`,
		alias,
	).Scan(&favorite, &notes, &last, &count, &loginOverride, &record)
	if errors.Is(err, sql.ErrNoRows) {
		return Meta{}, nil
	}
	if err != nil {
		return Meta{}, fmt.Errorf("store: get %s: %w", alias, err)
	}
	meta := Meta{
		Favorite:     favorite != 0,
		Notes:        notes,
		ConnectCount: count,
		Record:       record != 0,
	}
	if loginOverride.Valid {
		value := loginOverride.Int64 != 0
		meta.LoginOverride = &value
	}
	if last.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
			meta.LastConnectedAt = parsed
		}
	}
	tags, err := s.tags(alias)
	if err != nil {
		return Meta{}, err
	}
	meta.Tags = tags
	return meta, nil
}

// All returns all metadata keyed by alias.
func (s *Store) All() (map[string]Meta, error) {
	// Join tags in the same query so a large config does not need one query
	// per host. LEFT JOIN keeps hosts that have no tags.
	rows, err := s.db.Query(`
		SELECT m.alias, m.favorite, m.notes, m.last_connected_at,
		       m.connect_count, m.login_override, m.record, t.tag
		  FROM host_meta AS m
		  LEFT JOIN host_tags AS t ON t.alias = m.alias
		 ORDER BY m.alias, t.tag`)
	if err != nil {
		return nil, fmt.Errorf("store: list metadata: %w", err)
	}
	defer rows.Close()

	result := make(map[string]Meta)
	for rows.Next() {
		var (
			alias         string
			favorite      int
			notes         string
			last          sql.NullString
			count         int
			loginOverride sql.NullInt64
			record        int
			tag           sql.NullString
		)
		if err := rows.Scan(&alias, &favorite, &notes, &last, &count, &loginOverride, &record, &tag); err != nil {
			return nil, fmt.Errorf("store: scan metadata: %w", err)
		}
		meta, seen := result[alias]
		if !seen {
			meta = Meta{
				Favorite:     favorite != 0,
				Notes:        notes,
				ConnectCount: count,
				Record:       record != 0,
			}
			if loginOverride.Valid {
				value := loginOverride.Int64 != 0
				meta.LoginOverride = &value
			}
			if last.Valid {
				if parsed, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
					meta.LastConnectedAt = parsed
				}
			}
		}
		if tag.Valid {
			meta.Tags = append(meta.Tags, tag.String)
		}
		result[alias] = meta
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate metadata: %w", err)
	}
	return result, nil
}

func (s *Store) tags(alias string) ([]string, error) {
	rows, err := s.db.Query(`SELECT tag FROM host_tags WHERE alias = ? ORDER BY tag`, alias)
	if err != nil {
		return nil, fmt.Errorf("store: list tags for %s: %w", alias, err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("store: scan tag: %w", err)
		}
		tags = append(tags, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate tags: %w", err)
	}
	return tags, nil
}

// SetFavorite updates the favorite flag.
func (s *Store) SetFavorite(alias string, favorite bool) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("store: empty alias")
	}
	_, err := s.db.Exec(
		`INSERT INTO host_meta(alias, favorite) VALUES(?, ?)
		 ON CONFLICT(alias) DO UPDATE SET favorite = excluded.favorite`,
		alias, boolInt(favorite),
	)
	if err != nil {
		return fmt.Errorf("store: set favorite %s: %w", alias, err)
	}
	return nil
}

// SetLoginOverride stores the user's login/service override. Nil restores auto mode.
func (s *Store) SetLoginOverride(alias string, override *bool) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("store: empty alias")
	}
	var value any
	if override != nil {
		value = boolInt(*override)
	}
	_, err := s.db.Exec(
		`INSERT INTO host_meta(alias, login_override) VALUES(?, ?)
		 ON CONFLICT(alias) DO UPDATE SET login_override = excluded.login_override`,
		alias, value,
	)
	if err != nil {
		return fmt.Errorf("store: set login override %s: %w", alias, err)
	}
	return nil
}

// SetRecord stores whether interactive sessions for alias are recorded by default.
func (s *Store) SetRecord(alias string, enabled bool) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("store: empty alias")
	}
	_, err := s.db.Exec(
		`INSERT INTO host_meta(alias, record) VALUES(?, ?)
		 ON CONFLICT(alias) DO UPDATE SET record = excluded.record`,
		alias, boolInt(enabled),
	)
	if err != nil {
		return fmt.Errorf("store: set record %s: %w", alias, err)
	}
	return nil
}

// SetNotes updates notes.
func (s *Store) SetNotes(alias, notes string) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("store: empty alias")
	}
	_, err := s.db.Exec(
		`INSERT INTO host_meta(alias, notes) VALUES(?, ?)
		 ON CONFLICT(alias) DO UPDATE SET notes = excluded.notes`,
		alias, notes,
	)
	if err != nil {
		return fmt.Errorf("store: set notes %s: %w", alias, err)
	}
	return nil
}

// RenameHost moves metadata and tags from oldAlias to newAlias.
//
// It is a no-op when oldAlias has no metadata. Refusing to overwrite existing
// metadata prevents a rename from silently merging two hosts.
func (s *Store) RenameHost(oldAlias, newAlias string) error {
	oldAlias = strings.TrimSpace(oldAlias)
	newAlias = strings.TrimSpace(newAlias)
	if oldAlias == "" || newAlias == "" {
		return errors.New("store: empty alias")
	}
	if oldAlias == newAlias {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin rename %s: %w", oldAlias, err)
	}
	defer func() { _ = tx.Rollback() }()

	var oldExists, newExists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM host_meta WHERE alias = ?`, oldAlias).Scan(&oldExists); err != nil {
		return fmt.Errorf("store: check old alias %s: %w", oldAlias, err)
	}
	if oldExists == 0 {
		return nil
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM host_meta WHERE alias = ?`, newAlias).Scan(&newExists); err != nil {
		return fmt.Errorf("store: check new alias %s: %w", newAlias, err)
	}
	if newExists > 0 {
		return fmt.Errorf("store: rename %s: target metadata already exists for %s", oldAlias, newAlias)
	}
	if _, err := tx.Exec(`UPDATE host_meta SET alias = ? WHERE alias = ?`, newAlias, oldAlias); err != nil {
		return fmt.Errorf("store: rename metadata %s: %w", oldAlias, err)
	}
	if _, err := tx.Exec(`UPDATE host_tags SET alias = ? WHERE alias = ?`, newAlias, oldAlias); err != nil {
		return fmt.Errorf("store: rename tags %s: %w", oldAlias, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit rename %s: %w", oldAlias, err)
	}
	return nil
}

// SetTags replaces all tags for alias.
func (s *Store) SetTags(alias string, tags []string) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("store: empty alias")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin tags %s: %w", alias, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO host_meta(alias) VALUES(?)`, alias); err != nil {
		return fmt.Errorf("store: ensure metadata %s: %w", alias, err)
	}
	if _, err := tx.Exec(`DELETE FROM host_tags WHERE alias = ?`, alias); err != nil {
		return fmt.Errorf("store: clear tags %s: %w", alias, err)
	}
	seen := make(map[string]bool)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		if _, err := tx.Exec(`INSERT INTO host_tags(alias, tag) VALUES(?, ?)`, alias, tag); err != nil {
			return fmt.Errorf("store: insert tag %s: %w", tag, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tags %s: %w", alias, err)
	}
	return nil
}

// RecordConnect increments the connection count and updates the last-used time.
func (s *Store) RecordConnect(alias string, at time.Time) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("store: empty alias")
	}
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO host_meta(alias, last_connected_at, connect_count) VALUES(?, ?, 1)
		 ON CONFLICT(alias) DO UPDATE SET
			last_connected_at = excluded.last_connected_at,
			connect_count = host_meta.connect_count + 1`,
		alias, at.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store: record connect %s: %w", alias, err)
	}
	return nil
}

// SetSnippet creates or updates a named command snippet.
func (s *Store) SetSnippet(name, command string) error {
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	if name == "" {
		return errors.New("store: empty snippet name")
	}
	if command == "" {
		return errors.New("store: empty snippet command")
	}
	_, err := s.db.Exec(
		`INSERT INTO snippets(name, command, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET command = excluded.command, updated_at = excluded.updated_at`,
		name, command, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store: set snippet %s: %w", name, err)
	}
	return nil
}

// Snippets returns all snippets keyed by name.
func (s *Store) Snippets() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT name, command FROM snippets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list snippets: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var name, command string
		if err := rows.Scan(&name, &command); err != nil {
			return nil, fmt.Errorf("store: scan snippet: %w", err)
		}
		result[name] = command
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate snippets: %w", err)
	}
	return result, nil
}

// DeleteSnippet removes a snippet.
func (s *Store) DeleteSnippet(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("store: empty snippet name")
	}
	if _, err := s.db.Exec(`DELETE FROM snippets WHERE name = ?`, name); err != nil {
		return fmt.Errorf("store: delete snippet %s: %w", name, err)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// SortTags sorts and deduplicates tags in place.
func SortTags(tags []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}
