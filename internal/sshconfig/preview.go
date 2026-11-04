package sshconfig

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Preview is a read-only config edit preview. Hash identifies the concrete
// file content shown in the diff and must be passed back as Edit.ExpectedHash
// when applying the change.
type Preview struct {
	Diff string
	Hash string
}

// PreviewHost returns the unified diff that EditHost would produce for alias.
// See PreviewHostChange when the caller also needs the stale-preview guard.
func PreviewHost(path, alias string, edit Edit) (string, error) {
	preview, err := PreviewHostChange(path, alias, edit)
	if err != nil {
		return "", err
	}
	return preview.Diff, nil
}

// PreviewHostChange returns a unified diff plus a content hash for the concrete
// file being edited. Previewing is read-only: it does not lock, create backups,
// write temporary files, or replace the source config.
func PreviewHostChange(path, alias string, edit Edit) (Preview, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return Preview{}, fmt.Errorf("sshconfig: empty host alias")
	}
	if edit.Port != nil && (*edit.Port < 1 || *edit.Port > 65535) {
		return Preview{}, fmt.Errorf("sshconfig: invalid port %d", *edit.Port)
	}

	root := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		root = resolved
	}

	source, _, _, err := findHostSource(root, alias, 0, map[string]bool{})
	if err != nil {
		return Preview{}, err
	}
	target := source
	if resolved, err := filepath.EvalSymlinks(source); err == nil {
		target = resolved
	}

	original, err := os.ReadFile(target)
	if err != nil {
		return Preview{}, fmt.Errorf("sshconfig: read %s: %w", target, err)
	}

	updated, err := applyHostEdit(original, alias, edit)
	if err != nil {
		return Preview{}, err
	}
	if bytes.Equal(original, updated) {
		return Preview{}, nil
	}
	// Edits preserve CRLF on disk, but diffs are display-oriented and should
	// not put carriage-return control bytes into the terminal.
	normalized := strings.ReplaceAll(string(original), "\r\n", "\n")
	updatedDisplay := strings.ReplaceAll(string(updated), "\r\n", "\n")
	return Preview{
		Diff: previewUnifiedDiff(target, normalized, updatedDisplay),
		Hash: contentHash(original),
	}, nil
}

func contentHash(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

// previewUnifiedDiff renders a single-hunk unified diff. PreviewHost only
// changes one concrete Host block, so a common-prefix/common-suffix hunk keeps
// all changed lines together without pulling in an external diff dependency.
func previewUnifiedDiff(label, oldText, newText string) string {
	oldLines, oldFinalNewline := previewSplitLines(oldText)
	newLines, newFinalNewline := previewSplitLines(newText)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	// A missing final newline is represented by changing the final line.
	// This case cannot be produced by a Host field edit, but handling it keeps
	// the diff renderer internally consistent.
	if prefix == len(oldLines) && prefix == len(newLines) && oldFinalNewline != newFinalNewline && prefix > 0 {
		prefix--
		suffix = 0
	}

	if prefix == len(oldLines) && prefix == len(newLines) && oldFinalNewline == newFinalNewline {
		return ""
	}

	const contextLines = 3
	oldStart := max(0, prefix-contextLines)
	newStart := max(0, prefix-contextLines)
	oldEnd := min(len(oldLines), len(oldLines)-suffix+contextLines)
	newEnd := min(len(newLines), len(newLines)-suffix+contextLines)

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", previewDiffLabel(label), previewDiffLabel(label))
	fmt.Fprintf(&out, "@@ -%s +%s @@\n",
		previewHunkRange(oldStart, oldEnd-oldStart),
		previewHunkRange(newStart, newEnd-newStart))

	for i := oldStart; i < prefix; i++ {
		previewWriteDiffLine(&out, " ", oldLines[i], !oldFinalNewline && i == len(oldLines)-1)
	}
	for i := prefix; i < len(oldLines)-suffix; i++ {
		previewWriteDiffLine(&out, "-", oldLines[i], !oldFinalNewline && i == len(oldLines)-1)
	}
	for i := prefix; i < len(newLines)-suffix; i++ {
		previewWriteDiffLine(&out, "+", newLines[i], !newFinalNewline && i == len(newLines)-1)
	}
	for i := len(oldLines) - suffix; i < oldEnd; i++ {
		oldLast := !oldFinalNewline && i == len(oldLines)-1
		newLast := !newFinalNewline && i < len(newLines) && i == len(newLines)-1
		previewWriteDiffLine(&out, " ", oldLines[i], oldLast || newLast)
	}
	return out.String()
}

func previewSplitLines(text string) ([]string, bool) {
	if text == "" {
		return nil, false
	}
	finalNewline := strings.HasSuffix(text, "\n")
	if finalNewline {
		text = strings.TrimSuffix(text, "\n")
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines, finalNewline
}

func previewWriteDiffLine(out *strings.Builder, prefix, line string, noFinalNewline bool) {
	out.WriteString(prefix)
	out.WriteString(line)
	out.WriteByte('\n')
	if noFinalNewline {
		out.WriteString("\\ No newline at end of file\n")
	}
}

func previewHunkRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}

func previewDiffLabel(label string) string {
	label = strings.ReplaceAll(label, "\r", "\\r")
	return strings.ReplaceAll(label, "\n", "\\n")
}
