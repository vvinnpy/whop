package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	maxTranscriptQueryLength = 1024
	transcriptSearchLimit    = 50
)

var (
	// ErrEmptyTranscriptQuery means the caller supplied no searchable text.
	ErrEmptyTranscriptQuery = errors.New("store: empty transcript query")
	// ErrInvalidTranscriptQuery means the query is malformed or too large.
	ErrInvalidTranscriptQuery = errors.New("store: invalid transcript query")
)

// Transcript is one recording transcript to add to the full-text index.
type Transcript struct {
	SessionID string
	Path      string
	Host      string
	StartedAt time.Time
	Text      string
}

// TranscriptMatch is one indexed transcript search result.
type TranscriptMatch struct {
	Path      string
	Host      string
	StartedAt time.Time
	Snippet   string
}

// IndexTranscript inserts or replaces one transcript in the FTS5 index.
func (s *Store) IndexTranscript(transcript Transcript) error {
	transcript.SessionID = strings.TrimSpace(transcript.SessionID)
	transcript.Path = strings.TrimSpace(transcript.Path)
	transcript.Host = strings.TrimSpace(transcript.Host)
	if transcript.SessionID == "" {
		return errors.New("store: empty transcript session id")
	}
	if transcript.Path == "" {
		return errors.New("store: empty transcript path")
	}
	if transcript.Host == "" {
		return errors.New("store: empty transcript host")
	}
	if transcript.StartedAt.IsZero() {
		return errors.New("store: empty transcript start time")
	}
	if strings.TrimSpace(transcript.Text) == "" {
		return errors.New("store: empty transcript text")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin transcript index: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	startedAt := transcript.StartedAt.UTC().Format(time.RFC3339Nano)
	var rowID int64
	err = tx.QueryRow(
		`SELECT id FROM transcript_index WHERE session_id = ?`,
		transcript.SessionID,
	).Scan(&rowID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		result, insertErr := tx.Exec(
			`INSERT INTO transcript_index(session_id, path, host, started_at) VALUES(?, ?, ?, ?)`,
			transcript.SessionID, transcript.Path, transcript.Host, startedAt,
		)
		if insertErr != nil {
			return fmt.Errorf("store: insert transcript %s: %w", transcript.SessionID, insertErr)
		}
		rowID, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: transcript row id %s: %w", transcript.SessionID, err)
		}
	case err != nil:
		return fmt.Errorf("store: find transcript %s: %w", transcript.SessionID, err)
	default:
		if _, err := tx.Exec(
			`UPDATE transcript_index SET path = ?, host = ?, started_at = ? WHERE id = ?`,
			transcript.Path, transcript.Host, startedAt, rowID,
		); err != nil {
			return fmt.Errorf("store: update transcript %s: %w", transcript.SessionID, err)
		}
	}

	if _, err := tx.Exec(`DELETE FROM transcript_fts WHERE rowid = ?`, rowID); err != nil {
		return fmt.Errorf("store: clear transcript fts %s: %w", transcript.SessionID, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO transcript_fts(rowid, text) VALUES(?, ?)`,
		rowID, transcript.Text,
	); err != nil {
		return fmt.Errorf("store: index transcript %s: %w", transcript.SessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit transcript %s: %w", transcript.SessionID, err)
	}
	return nil
}

// SearchTranscripts searches indexed transcript text. User input is treated as
// a literal phrase rather than raw FTS5 syntax.
func (s *Store) SearchTranscripts(query string) ([]TranscriptMatch, error) {
	matchQuery, err := transcriptFTSQuery(query)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT transcript_index.path,
		        transcript_index.host,
		        transcript_index.started_at,
		        snippet(transcript_fts, 0, '>>>', '<<<', ' ... ', 16)
		   FROM transcript_fts
		   JOIN transcript_index ON transcript_index.id = transcript_fts.rowid
		  WHERE transcript_fts MATCH ?
		  ORDER BY bm25(transcript_fts), transcript_index.started_at DESC
		  LIMIT ?`,
		matchQuery, transcriptSearchLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: search transcripts: %w", err)
	}
	defer rows.Close()

	var result []TranscriptMatch
	for rows.Next() {
		var (
			path, host, startedAt, snippet string
		)
		if err := rows.Scan(&path, &host, &startedAt, &snippet); err != nil {
			return nil, fmt.Errorf("store: scan transcript match: %w", err)
		}
		started, err := time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse transcript start time: %w", err)
		}
		result = append(result, TranscriptMatch{
			Path:      path,
			Host:      host,
			StartedAt: started,
			Snippet:   snippet,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate transcript matches: %w", err)
	}
	return result, nil
}

func transcriptFTSQuery(query string) (string, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return "", ErrEmptyTranscriptQuery
	}
	if len(query) > maxTranscriptQueryLength {
		return "", fmt.Errorf("%w: query exceeds %d bytes", ErrInvalidTranscriptQuery, maxTranscriptQueryLength)
	}
	hasSearchableRune := false
	for _, value := range query {
		if unicode.IsControl(value) {
			return "", fmt.Errorf("%w: query contains control characters", ErrInvalidTranscriptQuery)
		}
		if unicode.IsLetter(value) || unicode.IsNumber(value) {
			hasSearchableRune = true
		}
	}
	if !hasSearchableRune {
		return "", fmt.Errorf("%w: query has no searchable characters", ErrInvalidTranscriptQuery)
	}
	// Quoting the whole input prevents FTS5 operators, column filters, and
	// prefix syntax from changing the meaning of user-provided text.
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`, nil
}
