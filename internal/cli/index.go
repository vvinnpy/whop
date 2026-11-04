package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"ttydeck/internal/session"
	"ttydeck/internal/store"
)

type indexResult struct {
	Directory string `json:"directory"`
	Indexed   int    `json:"indexed"`
	Removed   int    `json:"removed"`
}

func syncTranscriptIndex(metaStore MetaStore, dir string, skipInvalid, prune bool) ([]session.TranscriptDocument, int, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("stat recording directory: %w", err)
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("%s is not a directory", dir)
	}
	documents, err := session.IndexDirectoryWithOptions(dir, session.IndexOptions{SkipInvalid: skipInvalid})
	if err != nil {
		return nil, 0, err
	}
	paths := make([]string, 0, len(documents))
	for _, document := range documents {
		if err := metaStore.IndexTranscript(store.Transcript{
			SessionID: document.SessionID,
			Path:      document.Path,
			Host:      document.Host,
			StartedAt: document.StartedAt,
			Text:      document.Text,
		}); err != nil {
			return nil, 0, fmt.Errorf("index %s: %w", document.Path, err)
		}
		paths = append(paths, document.Path)
	}
	removed := 0
	if prune {
		removed, err = metaStore.ReconcileTranscriptRoot(dir, paths)
		if err != nil {
			return nil, 0, err
		}
	}
	return documents, removed, nil
}

func runIndex(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck index", deps)
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
		fmt.Fprintln(deps.Stderr, "usage: ttydeck index [flags]")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	documents, removed, err := syncTranscriptIndex(metaStore, *dir, *skipInvalid, !*noPrune)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	result := indexResult{Directory: *dir, Indexed: len(documents), Removed: removed}
	if *asJSON {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
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
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
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
