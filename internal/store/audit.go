package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const auditEventsTableDDL = `CREATE TABLE IF NOT EXISTS audit_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	at TEXT NOT NULL,
	type TEXT NOT NULL,
	host TEXT NOT NULL DEFAULT '',
	macro TEXT NOT NULL DEFAULT '',
	step TEXT NOT NULL DEFAULT '',
	rule TEXT NOT NULL DEFAULT '',
	attempt INTEGER NOT NULL DEFAULT 0,
	bytes INTEGER NOT NULL DEFAULT 0,
	message TEXT NOT NULL DEFAULT ''
)`

const (
	defaultAuditEventLimit = 100
	maxAuditEventLimit     = 1000
	auditTimeLayout        = "2006-01-02T15:04:05.000000000Z"
)

// AuditEvent is metadata about an automation action. It intentionally has no
// field for command text, terminal output, or credentials.
type AuditEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Host    string    `json:"host"`
	Macro   string    `json:"macro"`
	Step    string    `json:"step"`
	Rule    string    `json:"rule"`
	Attempt int       `json:"attempt"`
	Bytes   int       `json:"bytes"`
	Message string    `json:"message"`
}

// AppendAudit stores an audit event. A zero timestamp is replaced with the
// current UTC time. Type is required. Callers must not put secrets or
// transmitted/received content in Message.
func (s *Store) AppendAudit(event AuditEvent) error {
	if strings.TrimSpace(event.Type) == "" {
		return errors.New("store: empty audit event type")
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	if _, err := s.db.Exec(
		`INSERT INTO audit_events(
			at, type, host, macro, step, rule, attempt, bytes, message
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Time.UTC().Format(auditTimeLayout),
		event.Type,
		event.Host,
		event.Macro,
		event.Step,
		event.Rule,
		event.Attempt,
		event.Bytes,
		event.Message,
	); err != nil {
		return fmt.Errorf("store: append audit event %s: %w", event.Type, err)
	}
	return nil
}

// AuditEvents returns audit events in reverse chronological order. For equal
// timestamps, the most recently inserted event is returned first. A limit less
// than one defaults to 100; limits above 1000 are capped at 1000.
func (s *Store) AuditEvents(limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = defaultAuditEventLimit
	}
	if limit > maxAuditEventLimit {
		limit = maxAuditEventLimit
	}
	rows, err := s.db.Query(
		`SELECT at, type, host, macro, step, rule, attempt, bytes, message
		 FROM audit_events
		 ORDER BY at DESC, id DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list audit events: %w", err)
	}
	defer rows.Close()

	events := make([]AuditEvent, 0)
	for rows.Next() {
		var (
			at      string
			event   AuditEvent
			message sql.NullString
		)
		if err := rows.Scan(
			&at,
			&event.Type,
			&event.Host,
			&event.Macro,
			&event.Step,
			&event.Rule,
			&event.Attempt,
			&event.Bytes,
			&message,
		); err != nil {
			return nil, fmt.Errorf("store: scan audit event: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("store: parse audit event time %q: %w", at, err)
		}
		event.Time = parsed
		event.Message = message.String
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit events: %w", err)
	}
	return events, nil
}

// ClearAudit removes all audit events.
func (s *Store) ClearAudit() error {
	if _, err := s.db.Exec(`DELETE FROM audit_events`); err != nil {
		return fmt.Errorf("store: clear audit events: %w", err)
	}
	return nil
}
