package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TranscriptDocument is a recording transcript prepared for a search index.
// It intentionally contains only plain data so callers can map it to
// internal/store.Transcript without this package depending on store.
type TranscriptDocument struct {
	SessionID string
	Path      string
	Host      string
	StartedAt time.Time
	Text      string
}

// IndexOptions controls transcript directory indexing.
type IndexOptions struct {
	// SkipInvalid ignores unreadable or malformed recording pairs instead of
	// failing the whole scan. Directory traversal errors are still returned.
	SkipInvalid bool
}

// IndexDirectory scans dir recursively for .txt transcripts with a matching
// .cast metadata file and returns deterministic, newest-first documents.
// Only existing .txt files are indexed; a .cast without a same-name .txt is
// ignored. Missing cast metadata is tolerated and filename fallbacks are used.
func IndexDirectory(dir string) ([]TranscriptDocument, error) {
	return IndexDirectoryWithOptions(dir, IndexOptions{})
}

// IndexDirectoryWithOptions is IndexDirectory with explicit failure handling.
func IndexDirectoryWithOptions(dir string, opts IndexOptions) ([]TranscriptDocument, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: stat recording directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session: %s is not a directory", dir)
	}

	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("session: resolve recording directory: %w", err)
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("session: resolve recording directory symlink: %w", err)
	}
	root = filepath.Clean(root)
	rootID := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))[:16]

	var documents []TranscriptDocument
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".txt") {
			return nil
		}

		document, err := indexTranscriptPair(root, rootID, path)
		if err != nil {
			if opts.SkipInvalid {
				return nil
			}
			return err
		}
		if strings.TrimSpace(document.Text) != "" {
			documents = append(documents, document)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("session: index recording directory: %w", err)
	}

	sort.SliceStable(documents, func(i, j int) bool {
		if !documents[i].StartedAt.Equal(documents[j].StartedAt) {
			return documents[i].StartedAt.After(documents[j].StartedAt)
		}
		if documents[i].SessionID != documents[j].SessionID {
			return documents[i].SessionID < documents[j].SessionID
		}
		return documents[i].Path < documents[j].Path
	})
	return documents, nil
}

func indexTranscriptPair(root, rootID, transcriptPath string) (TranscriptDocument, error) {
	resolvedTranscript, err := resolvePathWithin(root, transcriptPath)
	if err != nil {
		return TranscriptDocument{}, fmt.Errorf("session: index transcript %s: %w", transcriptPath, err)
	}
	relative, err := filepath.Rel(root, resolvedTranscript)
	if err != nil {
		return TranscriptDocument{}, fmt.Errorf("session: index transcript %s: resolve relative path: %w", transcriptPath, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return TranscriptDocument{}, fmt.Errorf("session: index transcript %s: path escapes recording directory", transcriptPath)
	}

	info, err := os.Stat(resolvedTranscript)
	if err != nil {
		return TranscriptDocument{}, fmt.Errorf("session: index transcript %s: stat text: %w", transcriptPath, err)
	}
	if info.Size() > maxTranscriptFileBytes {
		return TranscriptDocument{}, fmt.Errorf("transcript exceeds %d bytes", maxTranscriptFileBytes)
	}
	text, err := os.ReadFile(resolvedTranscript)
	if err != nil {
		return TranscriptDocument{}, fmt.Errorf("session: index transcript %s: read text: %w", transcriptPath, err)
	}

	base := strings.TrimSuffix(relative, filepath.Ext(relative))
	if base == "" {
		base = relative
	}
	sessionID := rootID + ":" + filepath.ToSlash(base)

	castPath := strings.TrimSuffix(resolvedTranscript, filepath.Ext(resolvedTranscript)) + ".cast"
	resolvedCast, castResolveErr := resolvePathWithin(root, castPath)
	castErr := castResolveErr
	var meta indexCastMeta
	if castErr == nil {
		meta, castErr = readCastMetadata(resolvedCast)
	}
	if castErr != nil && !os.IsNotExist(castErr) {
		return TranscriptDocument{}, fmt.Errorf("session: index transcript %s: read cast %s: %w", transcriptPath, castPath, castErr)
	}

	startedAt := fileStartedAt(resolvedTranscript, base, meta, castErr == nil)
	host := strings.TrimSpace(meta.Env["WHOP_HOST"])
	if host == "" {
		host = hostFromBase(base)
	}
	if host == "" {
		host = "unknown"
	}

	return TranscriptDocument{
		SessionID: sessionID,
		Path:      filepath.Clean(resolvedTranscript),
		Host:      host,
		StartedAt: startedAt,
		Text:      string(text),
	}, nil
}

const maxTranscriptFileBytes = 64 << 20

func resolvePathWithin(root, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, filepath.Clean(resolved))
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		(len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes recording directory")
	}
	return filepath.Clean(resolved), nil
}

type indexCastMeta struct {
	Version   int               `json:"version"`
	Timestamp int64             `json:"timestamp"`
	Env       map[string]string `json:"env"`
}

// readCastMetadata reads the first JSON object of an asciicast stream. v2
// headers are the common case; v1 files and v2 headers without env are also
// accepted so legacy recordings remain indexable.
func readCastMetadata(path string) (indexCastMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return indexCastMeta{}, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	if prefix, err := reader.Peek(3); err == nil && string(prefix) == "\xef\xbb\xbf" {
		_, _ = reader.Discard(3)
	}
	var meta indexCastMeta
	if err := json.NewDecoder(reader).Decode(&meta); err != nil {
		return indexCastMeta{}, fmt.Errorf("decode header: %w", err)
	}
	if meta.Version != 1 && meta.Version != 2 {
		return indexCastMeta{}, fmt.Errorf("unsupported asciicast version %d", meta.Version)
	}
	return meta, nil
}

var recordingBasePattern = regexp.MustCompile(`^(.*)-(\d{8})-(\d{6})(?:\.(\d{1,9}))?$`)

// hostFromBase recovers the sanitized alias from the recording filename format
// used by NewFileRecording: <alias>-YYYYMMDD-HHMMSS.mmm.
func hostFromBase(base string) string {
	base = filepath.Base(filepath.FromSlash(base))
	match := recordingBasePattern.FindStringSubmatch(base)
	if match == nil || match[1] == "" {
		return strings.TrimSpace(base)
	}
	return strings.TrimSpace(match[1])
}

func fileStartedAt(transcriptPath, base string, meta indexCastMeta, haveCast bool) time.Time {
	if haveCast && meta.Timestamp > 0 {
		return time.Unix(meta.Timestamp, 0).UTC()
	}
	if started, ok := startedAtFromBase(base); ok {
		return started.UTC()
	}
	info, err := os.Stat(transcriptPath)
	if err == nil && !info.ModTime().IsZero() {
		return info.ModTime().UTC()
	}
	return time.Time{}
}

func startedAtFromBase(base string) (time.Time, bool) {
	name := filepath.Base(filepath.FromSlash(base))
	match := recordingBasePattern.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false
	}
	fraction := match[4]
	if fraction == "" {
		fraction = "0"
	} else if len(fraction) < 9 {
		fraction += strings.Repeat("0", 9-len(fraction))
	} else if len(fraction) > 9 {
		fraction = fraction[:9]
	}
	nanoseconds, err := strconv.Atoi(fraction)
	if err != nil {
		return time.Time{}, false
	}
	started, err := time.ParseInLocation("20060102-150405", match[2]+"-"+match[3], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return started.Add(time.Duration(nanoseconds)), true
}
