package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
// Size and ModTime are optional source-file fingerprints. When both are set,
// IndexTranscript skips unchanged rows instead of rebuilding their FTS data.
type Transcript struct {
	SessionID string
	Path      string
	Host      string
	StartedAt time.Time
	Size      int64
	ModTime   time.Time
	Text      string
}

// TranscriptMatch is one indexed transcript search result.
type TranscriptMatch struct {
	Path      string
	Host      string
	StartedAt time.Time
	Snippet   string
}

// TranscriptFingerprint describes the source file recorded for an indexed
// transcript. Callers can compare it before reading a file so unchanged
// transcripts can be skipped entirely.
type TranscriptFingerprint struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// IndexTranscript inserts or replaces one transcript in the FTS5 index. It
// returns true when the FTS row was written and false when the stored row
// already matched the transcript fingerprint.
func (s *Store) IndexTranscript(transcript Transcript) (bool, error) {
	transcript.SessionID = strings.TrimSpace(transcript.SessionID)
	transcript.Path = strings.TrimSpace(transcript.Path)
	transcript.Host = strings.TrimSpace(transcript.Host)
	if transcript.SessionID == "" {
		return false, errors.New("store: empty transcript session id")
	}
	if transcript.Path == "" {
		return false, errors.New("store: empty transcript path")
	}
	if transcript.Host == "" {
		return false, errors.New("store: empty transcript host")
	}
	if transcript.StartedAt.IsZero() {
		return false, errors.New("store: empty transcript start time")
	}
	if strings.TrimSpace(transcript.Text) == "" {
		return false, errors.New("store: empty transcript text")
	}

	fileSize, fileMTime, hasFingerprint := transcriptFingerprint(transcript)

	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("store: begin transcript index: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	startedAt := transcript.StartedAt.UTC().Format(time.RFC3339Nano)
	var (
		rowID           int64
		existingPath    string
		existingHost    string
		existingStarted string
		existingSize    int64
		existingMTime   int64
	)
	err = tx.QueryRow(
		`SELECT id, path, host, started_at, file_size, file_mtime
		   FROM transcript_index WHERE session_id = ?`,
		transcript.SessionID,
	).Scan(&rowID, &existingPath, &existingHost, &existingStarted, &existingSize, &existingMTime)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		result, insertErr := tx.Exec(
			`INSERT INTO transcript_index(
				session_id, path, host, started_at, file_size, file_mtime
			) VALUES(?, ?, ?, ?, ?, ?)`,
			transcript.SessionID, transcript.Path, transcript.Host, startedAt, fileSize, fileMTime,
		)
		if insertErr != nil {
			return false, fmt.Errorf("store: insert transcript %s: %w", transcript.SessionID, insertErr)
		}
		rowID, err = result.LastInsertId()
		if err != nil {
			return false, fmt.Errorf("store: transcript row id %s: %w", transcript.SessionID, err)
		}
	case err != nil:
		return false, fmt.Errorf("store: find transcript %s: %w", transcript.SessionID, err)
	default:
		if hasFingerprint &&
			existingPath == transcript.Path &&
			existingHost == transcript.Host &&
			existingStarted == startedAt &&
			existingSize == fileSize &&
			existingMTime == fileMTime {
			return false, nil
		}
		if _, err := tx.Exec(
			`UPDATE transcript_index
			    SET path = ?, host = ?, started_at = ?, file_size = ?, file_mtime = ?
			  WHERE id = ?`,
			transcript.Path, transcript.Host, startedAt, fileSize, fileMTime, rowID,
		); err != nil {
			return false, fmt.Errorf("store: update transcript %s: %w", transcript.SessionID, err)
		}
	}

	if _, err := tx.Exec(`DELETE FROM transcript_fts_trigram WHERE rowid = ?`, rowID); err != nil {
		return false, fmt.Errorf("store: clear transcript fts %s: %w", transcript.SessionID, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO transcript_fts_trigram(rowid, text) VALUES(?, ?)`,
		rowID, transcript.Text,
	); err != nil {
		return false, fmt.Errorf("store: index transcript %s: %w", transcript.SessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: commit transcript %s: %w", transcript.SessionID, err)
	}
	return true, nil
}

func transcriptFingerprint(transcript Transcript) (size, mtime int64, ok bool) {
	if transcript.Size <= 0 || transcript.ModTime.IsZero() {
		return -1, 0, false
	}
	return transcript.Size, transcript.ModTime.UTC().UnixNano(), true
}

// TranscriptFingerprints returns the last indexed source-file fingerprints
// keyed by transcript path.
func (s *Store) TranscriptFingerprints() (map[string]TranscriptFingerprint, error) {
	rows, err := s.db.Query(
		`SELECT path, file_size, file_mtime
		   FROM transcript_index
		  WHERE file_size > 0 AND file_mtime <> 0`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list transcript fingerprints: %w", err)
	}
	defer rows.Close()

	result := make(map[string]TranscriptFingerprint)
	for rows.Next() {
		var (
			path    string
			size    int64
			mtimeNS int64
		)
		if err := rows.Scan(&path, &size, &mtimeNS); err != nil {
			return nil, fmt.Errorf("store: scan transcript fingerprint: %w", err)
		}
		result[path] = TranscriptFingerprint{
			Path:    path,
			Size:    size,
			ModTime: time.Unix(0, mtimeNS).UTC(),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate transcript fingerprints: %w", err)
	}
	return result, nil
}

// SearchTranscripts searches indexed transcript text. User input is treated as
// a literal phrase rather than raw FTS5 syntax.
func (s *Store) SearchTranscripts(query string) ([]TranscriptMatch, error) {
	normalized := strings.Join(strings.Fields(query), " ")
	matchQuery, err := transcriptFTSQuery(normalized)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(normalized) < 3 {
		return s.searchShortTranscriptQuery(normalized)
	}
	rows, err := s.db.Query(
		`SELECT transcript_index.path,
		        transcript_index.host,
		        transcript_index.started_at,
		        snippet(transcript_fts_trigram, 0, '>>>', '<<<', ' ... ', 16)
		   FROM transcript_fts_trigram
		   JOIN transcript_index ON transcript_index.id = transcript_fts_trigram.rowid
		  WHERE transcript_fts_trigram MATCH ?
		  ORDER BY bm25(transcript_fts_trigram), transcript_index.started_at DESC
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

func (s *Store) searchShortTranscriptQuery(query string) ([]TranscriptMatch, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	rows, err := s.db.Query(
		`SELECT transcript_index.path,
		        transcript_index.host,
		        transcript_index.started_at,
		        substr(transcript_fts_trigram.text,
		               max(1, instr(lower(transcript_fts_trigram.text), lower(?)) - 24),
		               80)
		   FROM transcript_fts_trigram
		   JOIN transcript_index ON transcript_index.id = transcript_fts_trigram.rowid
		  WHERE transcript_fts_trigram.text LIKE '%' || ? || '%' ESCAPE '\'
		  ORDER BY transcript_index.started_at DESC
		  LIMIT ?`,
		query, escaped, transcriptSearchLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: search short transcripts: %w", err)
	}
	defer rows.Close()
	var result []TranscriptMatch
	for rows.Next() {
		var path, host, startedAt, snippet string
		if err := rows.Scan(&path, &host, &startedAt, &snippet); err != nil {
			return nil, fmt.Errorf("store: scan short transcript match: %w", err)
		}
		started, err := time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse transcript start time: %w", err)
		}
		result = append(result, TranscriptMatch{Path: path, Host: host, StartedAt: started, Snippet: snippet})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate short transcript matches: %w", err)
	}
	return result, nil
}
