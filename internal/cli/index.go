package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"whop/internal/session"
	"whop/internal/store"
)

type indexResult struct {
	Directory string `json:"directory"`
	Indexed   int    `json:"indexed"`
	Removed   int    `json:"removed"`
}

func syncTranscriptIndex(metaStore MetaStore, dir string, skipInvalid, prune bool) (int, int, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("stat recording directory: %w", err)
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("%s is not a directory", dir)
	}
	fingerprints, err := metaStore.TranscriptFingerprints()
	if err != nil {
		return 0, 0, fmt.Errorf("load transcript fingerprints: %w", err)
	}
	documents, err := session.IndexDirectoryWithOptions(dir, session.IndexOptions{
		SkipInvalid: skipInvalid,
		SkipText: func(path string, info fs.FileInfo) bool {
			fingerprint, ok := fingerprints[filepath.Clean(path)]
			if !ok {
				return false
			}
			return fingerprint.Size == info.Size() &&
				fingerprint.ModTime.UnixNano() == info.ModTime().UTC().UnixNano()
		},
	})
	if err != nil {
		return 0, 0, err
	}
	paths := make([]string, 0, len(documents))
	indexed := 0
	for _, document := range documents {
		paths = append(paths, document.Path)
		if document.Skipped {
			continue
		}
		changed, err := metaStore.IndexTranscript(store.Transcript{
			SessionID: document.SessionID,
			Path:      document.Path,
			Host:      document.Host,
			StartedAt: document.StartedAt,
			Size:      document.Size,
			ModTime:   document.ModTime,
			Text:      document.Text,
		})
		if err != nil {
			return 0, 0, fmt.Errorf("index %s: %w", document.Path, err)
		}
		if changed {
			indexed++
		}
	}
	removed := 0
	if prune {
		removed, err = metaStore.ReconcileTranscriptRoot(dir, paths)
		if err != nil {
			return 0, 0, err
		}
	}
	return indexed, removed, nil
}

func runIndex(args []string, deps Deps) int {
	fs := newFlagSet("whop index", deps)
	dir := fs.String("dir", deps.RecordingDir, "recording directory")
	skipInvalid := fs.Bool("skip-invalid", false, "skip malformed recordings")
	noPrune := fs.Bool("no-prune", false, "keep index entries for removed recordings")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop index [flags]")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	indexed, removed, err := syncTranscriptIndex(metaStore, *dir, *skipInvalid, !*noPrune)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	result := indexResult{Directory: *dir, Indexed: indexed, Removed: removed}
	if *asJSON {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	fmt.Fprintf(deps.Stdout, "indexed %d transcript(s), removed %d stale entr(ies) from %s\n", result.Indexed, result.Removed, result.Directory)
	return 0
}

func writeTranscriptMatches(deps Deps, matches []store.TranscriptMatch, asJSON bool) int {
	if asJSON {
		if matches == nil {
			matches = []store.TranscriptMatch{}
		}
		encoded, err := json.MarshalIndent(matches, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	for _, match := range matches {
		snippet := sanitizeSingleLine(match.Snippet)
		fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\t%s\n",
			match.StartedAt.Local().Format("2006-01-02 15:04:05 -0700"),
			sanitizeSingleLine(match.Host), sanitizeSingleLine(match.Path), snippet)
	}
	return 0
}

func sanitizeSingleLine(value string) string {
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
