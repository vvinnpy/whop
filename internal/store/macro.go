package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const macroTableDDL = `CREATE TABLE IF NOT EXISTS macro_defs (
	name TEXT PRIMARY KEY,
	definition TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

// SetMacroDefinition creates or replaces a macro definition. definition is
// stored verbatim as caller-owned JSON; it is never parsed or inspected.
func (s *Store) SetMacroDefinition(name string, definition []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("store: empty macro name")
	}
	if len(bytes.TrimSpace(definition)) == 0 {
		return errors.New("store: empty macro definition")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin macro definition %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO macro_defs(name, definition, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
			definition = excluded.definition,
			updated_at = excluded.updated_at`,
		name, string(definition), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("store: set macro definition %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit macro definition %s: %w", name, err)
	}
	return nil
}

// MacroDefinition returns the stored definition for name. A missing macro
// returns found=false and no error.
func (s *Store) MacroDefinition(name string) ([]byte, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, errors.New("store: empty macro name")
	}
	var definition string
	err := s.db.QueryRow(
		`SELECT definition FROM macro_defs WHERE name = ?`,
		name,
	).Scan(&definition)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: get macro definition %s: %w", name, err)
	}
	return []byte(definition), true, nil
}

// MacroDefinitions returns all macro definitions keyed by name.
func (s *Store) MacroDefinitions() (map[string][]byte, error) {
	rows, err := s.db.Query(`SELECT name, definition FROM macro_defs ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list macro definitions: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]byte)
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			return nil, fmt.Errorf("store: scan macro definition: %w", err)
		}
		result[name] = []byte(definition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate macro definitions: %w", err)
	}
	return result, nil
}

// DeleteMacroDefinition removes a macro definition. Deleting a missing macro
// is not an error.
func (s *Store) DeleteMacroDefinition(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("store: empty macro name")
	}
	if _, err := s.db.Exec(`DELETE FROM macro_defs WHERE name = ?`, name); err != nil {
		return fmt.Errorf("store: delete macro definition %s: %w", name, err)
	}
	return nil
}
