package session

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Match is one transcript search hit.
type Match struct {
	Path string
	Line int
	Text string
}

// Search scans .txt transcripts under dir for query.
func Search(dir, query string) ([]Match, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
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

	var matches []Match
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".txt") {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
				matches = append(matches, Match{Path: path, Line: lineNumber, Text: line})
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("session: search transcripts: %w", err)
	}
	return matches, nil
}
