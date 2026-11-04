package sshconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/kevinburke/ssh_config"
	"github.com/natefinch/atomic"
)

var (
	// ErrHostNotFound means the requested alias has no concrete Host block.
	ErrHostNotFound = fmt.Errorf("sshconfig: host not found")
	// ErrConfigChanged means the file changed while we were editing it.
	ErrConfigChanged = fmt.Errorf("sshconfig: config changed while editing")
)

// Edit describes safe changes to a single concrete Host block.
type Edit struct {
	HostName     *string
	User         *string
	Port         *int
	ProxyJump    *string
	IdentityFile *string
	Tags         *[]string
	// ExpectedHash is the concrete source-file hash returned by
	// PreviewHostChange. When set, EditHost refuses to apply a stale preview.
	ExpectedHash string
}

// EditHost edits one Host block in place while preserving unrelated text.
//
// It uses an advisory lock, a pre-write conflict check, a timestamped backup,
// a temporary file, and an atomic replace. If the alias is defined in an
// Include file, EditHost edits that file in place and leaves the root config
// untouched.
func EditHost(path, alias string, edit Edit) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("sshconfig: empty host alias")
	}
	if edit.Port != nil && (*edit.Port < 1 || *edit.Port > 65535) {
		return fmt.Errorf("sshconfig: invalid port %d", *edit.Port)
	}

	root := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		root = resolved
	}

	// All edits rooted at the same config tree share one lock, even when the
	// concrete Host lives in an Include file.
	lock := flock.New(root + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("sshconfig: lock %s: %w", root, err)
	}
	defer func() { _ = lock.Unlock() }()

	source, _, _, err := findHostSource(root, alias, 0, map[string]bool{})
	if err != nil {
		return err
	}
	target := source
	if resolved, err := filepath.EvalSymlinks(source); err == nil {
		target = resolved
	}

	original, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("sshconfig: read %s: %w", target, err)
	}
	if edit.ExpectedHash != "" && contentHash(original) != edit.ExpectedHash {
		return fmt.Errorf("%w: host %s changed since preview", ErrConfigChanged, alias)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("sshconfig: stat %s: %w", target, err)
	}

	updated, err := applyHostEdit(original, alias, edit)
	if err != nil {
		return err
	}

	current, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("sshconfig: re-read %s: %w", target, err)
	}
	if !bytes.Equal(current, original) {
		return ErrConfigChanged
	}

	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	backup := fmt.Sprintf("%s.bak.%s", target, time.Now().Format("20060102-150405.000"))
	if err := os.WriteFile(backup, original, mode); err != nil {
		return fmt.Errorf("sshconfig: backup %s: %w", backup, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".ttydeck-config-*")
	if err != nil {
		return fmt.Errorf("sshconfig: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sshconfig: chmod temporary file: %w", err)
	}
	if _, err := tmp.Write(updated); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sshconfig: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sshconfig: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("sshconfig: close temporary file: %w", err)
	}
	if err := atomic.ReplaceFile(tmpName, target); err != nil {
		cleanup()
		return fmt.Errorf("sshconfig: replace %s: %w", target, err)
	}
	return nil
}

// applyHostEdit applies one Edit to raw config bytes. EditHost and PreviewHost
// share this function so a preview is produced by exactly the same transform
// that is later persisted.
func applyHostEdit(original []byte, alias string, edit Edit) ([]byte, error) {
	lines := strings.Split(string(original), "\n")
	start, end, err := findHostBlock(lines, alias)
	if err != nil {
		return nil, err
	}
	if edit.HostName != nil {
		setKV(&lines, &start, &end, "HostName", *edit.HostName)
	}
	if edit.User != nil {
		setKV(&lines, &start, &end, "User", *edit.User)
	}
	if edit.Port != nil {
		setKV(&lines, &start, &end, "Port", strconv.Itoa(*edit.Port))
	}
	if edit.ProxyJump != nil {
		setKV(&lines, &start, &end, "ProxyJump", *edit.ProxyJump)
	}
	if edit.IdentityFile != nil {
		replaceAllKV(&lines, &start, &end, "IdentityFile", *edit.IdentityFile)
	}
	if edit.Tags != nil {
		applyTags(&lines, &start, &end, *edit.Tags)
	}
	updated := strings.Join(lines, "\n")
	if _, err := ssh_config.Decode(strings.NewReader(updated)); err != nil {
		return nil, fmt.Errorf("sshconfig: edited config is invalid: %w", err)
	}
	if _, _, err := findHostBlock(strings.Split(updated, "\n"), alias); err != nil {
		return nil, fmt.Errorf("sshconfig: edited config lost host %s: %w", alias, err)
	}
	return []byte(updated), nil
}

func findHostBlock(lines []string, alias string) (start, end int, err error) {
	for i, line := range lines {
		if !isHostLine(line) {
			continue
		}
		patterns := hostPatterns(line)
		matched := false
		for _, pattern := range patterns {
			if strings.EqualFold(cleanPattern(pattern), alias) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		start = i
		end = len(lines)
		for j := i + 1; j < len(lines); j++ {
			if isHostLine(lines[j]) || isMatchLine(lines[j]) {
				end = j
				break
			}
		}
		return start, end, nil
	}
	return 0, 0, fmt.Errorf("%w: %s", ErrHostNotFound, alias)
}

func findHostSource(path, alias string, depth int, stack map[string]bool) (source string, start, end int, err error) {
	if depth > 5 {
		return "", 0, 0, fmt.Errorf("sshconfig: Include depth exceeded at %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, 0, fmt.Errorf("sshconfig: resolve %s: %w", path, err)
	}
	if stack[abs] {
		return "", 0, 0, fmt.Errorf("sshconfig: recursive Include at %s", path)
	}
	stack[abs] = true
	defer delete(stack, abs)

	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, 0, fmt.Errorf("sshconfig: read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if isIncludeLine(line) {
			patterns, err := includePatterns(line)
			if err != nil {
				return "", 0, 0, fmt.Errorf("sshconfig: %s: %w", path, err)
			}
			for _, pattern := range patterns {
				matches, err := expandIncludePattern(pattern, filepath.Dir(path))
				if err != nil {
					return "", 0, 0, fmt.Errorf("sshconfig: %s: %w", path, err)
				}
				for _, match := range matches {
					found, foundStart, foundEnd, err := findHostSource(match, alias, depth+1, stack)
					if err == nil {
						return found, foundStart, foundEnd, nil
					}
					if !errors.Is(err, ErrHostNotFound) {
						return "", 0, 0, err
					}
				}
			}
			continue
		}
		if !isHostLine(line) {
			continue
		}
		for _, pattern := range hostPatterns(line) {
			if !strings.EqualFold(cleanPattern(pattern), alias) {
				continue
			}
			start, end, err := findHostBlock(lines, alias)
			if err != nil {
				return "", 0, 0, err
			}
			return path, start, end, nil
		}
	}
	return "", 0, 0, fmt.Errorf("%w: %s", ErrHostNotFound, alias)
}

func isHostLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return lower == "host" || strings.HasPrefix(lower, "host ") || strings.HasPrefix(lower, "host=")
}

func isMatchLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return lower == "match" || strings.HasPrefix(lower, "match ") || strings.HasPrefix(lower, "match=")
}

func hostPatterns(line string) []string {
	trimmed := strings.TrimSpace(line)
	rest := strings.TrimSpace(trimmed[len("Host"):])
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "="))
	if rest == "" {
		return nil
	}
	return strings.Fields(rest)
}

func setKV(lines *[]string, start, end *int, key, value string) {
	for i := *start + 1; i < *end; i++ {
		if keyMatches((*lines)[i], key) {
			(*lines)[i] = rewriteKV((*lines)[i], key, value)
			return
		}
	}
	insertBlockLine(lines, start, end, "  "+key+" "+value)
}

func replaceAllKV(lines *[]string, start, end *int, key, value string) {
	kept := make([]string, 0, len(*lines))
	for i, line := range *lines {
		if i > *start && i < *end && keyMatches(line, key) {
			continue
		}
		kept = append(kept, line)
	}
	removed := len(*lines) - len(kept)
	*lines = kept
	*end -= removed
	insertBlockLine(lines, start, end, "  "+key+" "+value)
}

func insertBlockLine(lines *[]string, start, end *int, line string) {
	insertAt := *end
	for insertAt > *start+1 && strings.TrimSpace((*lines)[insertAt-1]) == "" {
		insertAt--
	}
	updated := make([]string, 0, len(*lines)+1)
	updated = append(updated, (*lines)[:insertAt]...)
	updated = append(updated, line)
	updated = append(updated, (*lines)[insertAt:]...)
	*lines = updated
	*end++
}

func keyMatches(line, key string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	lower := strings.ToLower(trimmed)
	key = strings.ToLower(key)
	if !strings.HasPrefix(lower, key) {
		return false
	}
	if len(trimmed) == len(key) {
		return true
	}
	next := trimmed[len(key)]
	return next == ' ' || next == '\t' || next == '='
}

func rewriteKV(line, key, value string) string {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	body := strings.TrimLeft(line, " \t")
	comment := ""
	if index := strings.Index(body, "#"); index >= 0 {
		comment = body[index:]
		body = strings.TrimSpace(body[:index])
	}
	separator := " "
	if strings.Contains(body, "=") {
		separator = " = "
	}
	updated := indent + key + separator + value
	if comment != "" {
		updated += " " + comment
	}
	return updated
}

func applyTags(lines *[]string, start, end *int, tags []string) {
	value := strings.Join(normalizeTags(tags), ",")
	const marker = "ttydeck:tags="
	for i := *start + 1; i < *end; i++ {
		line := (*lines)[i]
		index := strings.Index(strings.ToLower(line), marker)
		if index < 0 {
			continue
		}
		before := line[:index]
		after := line[index+len(marker):]
		suffix := ""
		if fields := strings.Fields(after); len(fields) > 0 {
			if offset := strings.Index(after, fields[0]); offset >= 0 {
				suffix = after[offset+len(fields[0]):]
			}
		}
		(*lines)[i] = before + marker + value + suffix
		return
	}
	insertBlockLine(lines, start, end, "  # "+marker+value)
}

func normalizeTags(tags []string) []string {
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
	return out
}
