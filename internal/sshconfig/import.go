package sshconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gofrs/flock"
	"github.com/natefinch/atomic"
)

// HostImport describes one Host block to append to an OpenSSH client config.
// Port must be 1-65535; the default Port 22 is omitted from the generated block.
// Empty User or IdentityFile fields are omitted. LegacySSHRSA enables the two
// opt-in ssh-rsa algorithm settings.
type HostImport struct {
	Alias               string
	HostName            string
	User                string
	IdentityFile        string
	Comment             string
	Port                int
	ServerAliveInterval int
	Compression         bool
	LegacySSHRSA        bool
}

// ImportResult reports the aliases added and skipped by AddHosts. Backup is the
// timestamped backup path created before the root config was replaced. It is
// empty when nothing was written.
type ImportResult struct {
	Added   []string
	Skipped []string
	Backup  string
}

// AddHosts safely appends concrete Host blocks to an OpenSSH client config.
//
// Existing aliases in the root config or any recursively Included file are
// skipped, as are duplicate aliases in imports. If there is nothing to add,
// AddHosts is read-only and does not create a lock file or backup. Otherwise it
// locks the config, checks for concurrent changes, creates a timestamped backup,
// fsyncs a same-directory temporary file, and atomically replaces the real
// target. A symlink passed as path remains a symlink.
func AddHosts(path string, imports []HostImport) (ImportResult, error) {
	result := ImportResult{Added: []string{}, Skipped: []string{}}
	if len(imports) == 0 {
		return result, nil
	}
	if strings.TrimSpace(path) == "" {
		return result, errors.New("sshconfig: empty config path")
	}
	for i := range imports {
		if err := validateHostImport(imports[i]); err != nil {
			return result, err
		}
	}

	target, err := resolveImportTarget(path)
	if err != nil {
		return result, err
	}

	// Avoid creating even the advisory lock file for the common no-op case.
	added, skipped, err := classifyHostImports(target, imports)
	if err != nil {
		return result, err
	}
	if len(added) == 0 {
		result.Skipped = skipped
		return result, nil
	}

	lock := flock.New(target + ".lock")
	if err := lock.Lock(); err != nil {
		return result, fmt.Errorf("sshconfig: lock %s: %w", target, err)
	}
	defer func() { _ = lock.Unlock() }()

	// Re-check while holding the lock so a cooperating writer cannot race us
	// between the initial no-op check and the append.
	added, skipped, err = classifyHostImports(target, imports)
	if err != nil {
		return result, err
	}
	result.Skipped = skipped
	if len(added) == 0 {
		return result, nil
	}

	original, mode, exists, err := readImportTarget(target)
	if err != nil {
		return result, err
	}
	updated := appendHostImports(original, added)

	// The lock is advisory. Re-read immediately before replacing so a config
	// changed by an editor that does not honor the lock is not overwritten.
	current, _, currentExists, err := readImportTarget(target)
	if err != nil {
		return result, err
	}
	if currentExists != exists || !bytes.Equal(current, original) {
		return result, fmt.Errorf("%w: %s changed during import", ErrConfigChanged, target)
	}

	backup, err := writeImportBackup(target, original, mode)
	if err != nil {
		return result, err
	}
	result.Backup = backup

	if err := replaceImportTarget(target, updated, mode); err != nil {
		return result, err
	}
	result.Added = make([]string, 0, len(added))
	for _, imp := range added {
		result.Added = append(result.Added, imp.Alias)
	}
	return result, nil
}

func validateHostImport(imp HostImport) error {
	if strings.TrimSpace(imp.Alias) == "" {
		return errors.New("sshconfig: import alias is required")
	}
	if strings.ContainsAny(imp.Alias, "*?![#") || strings.ContainsRune(imp.Alias, ']') {
		return fmt.Errorf("sshconfig: invalid import alias %q", imp.Alias)
	}
	if strings.IndexFunc(imp.Alias, unicode.IsSpace) >= 0 || hasControlOrLineBreak(imp.Alias) {
		return fmt.Errorf("sshconfig: invalid import alias %q", imp.Alias)
	}
	if strings.TrimSpace(imp.HostName) == "" {
		return fmt.Errorf("sshconfig: import %s requires a hostname", imp.Alias)
	}
	if strings.IndexFunc(imp.HostName, unicode.IsSpace) >= 0 || hasControlOrLineBreak(imp.HostName) {
		return fmt.Errorf("sshconfig: invalid hostname %q for %s", imp.HostName, imp.Alias)
	}
	if strings.ContainsRune(imp.HostName, '#') {
		return fmt.Errorf("sshconfig: invalid hostname %q for %s", imp.HostName, imp.Alias)
	}
	if imp.Port < 1 || imp.Port > 65535 {
		return fmt.Errorf("sshconfig: invalid port %d for %s", imp.Port, imp.Alias)
	}
	if imp.ServerAliveInterval < 0 {
		return fmt.Errorf("sshconfig: invalid ServerAliveInterval %d for %s", imp.ServerAliveInterval, imp.Alias)
	}
	if hasControlOrLineBreak(imp.User) {
		return fmt.Errorf("sshconfig: invalid user for %s", imp.Alias)
	}
	if hasControlOrLineBreak(imp.IdentityFile) {
		return fmt.Errorf("sshconfig: invalid IdentityFile for %s", imp.Alias)
	}
	if hasControlOrLineBreak(imp.Comment) {
		return fmt.Errorf("sshconfig: invalid comment for %s", imp.Alias)
	}
	return nil
}

func hasControlOrLineBreak(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

func resolveImportTarget(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("sshconfig: resolve %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("sshconfig: resolve symlink %s: %w", path, err)
	}

	// A broken symlink must not be replaced as though it were the config.
	if info, lerr := os.Lstat(abs); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("sshconfig: broken config symlink %s: %w", path, err)
	}

	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
	if parentErr != nil {
		return "", fmt.Errorf("sshconfig: resolve config directory for %s: %w", path, parentErr)
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func classifyHostImports(target string, imports []HostImport) ([]HostImport, []string, error) {
	existing := []Host{}
	if _, err := os.Stat(target); err == nil {
		hosts, err := Load(target)
		if err != nil {
			return nil, nil, err
		}
		existing = hosts
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("sshconfig: stat %s: %w", target, err)
	}

	seen := make(map[string]bool, len(imports))
	added := make([]HostImport, 0, len(imports))
	skipped := make([]string, 0)
	for _, imp := range imports {
		key := strings.ToLower(imp.Alias)
		if seen[key] || aliasInHosts(existing, imp.Alias) {
			skipped = append(skipped, imp.Alias)
			continue
		}
		seen[key] = true
		added = append(added, imp)
	}
	return added, skipped, nil
}

func aliasInHosts(hosts []Host, alias string) bool {
	for _, host := range hosts {
		if strings.EqualFold(host.Alias, alias) {
			return true
		}
	}
	return false
}

func readImportTarget(target string) ([]byte, fs.FileMode, bool, error) {
	info, err := os.Stat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("sshconfig: stat %s: %w", target, err)
	}
	if info.IsDir() {
		return nil, 0, false, fmt.Errorf("sshconfig: %s is a directory", target)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, 0, false, fmt.Errorf("sshconfig: read %s: %w", target, err)
	}
	mode := info.Mode().Perm() | 0o600
	return data, mode, true, nil
}

func appendHostImports(original []byte, imports []HostImport) []byte {
	ending := "\n"
	if bytes.Contains(original, []byte("\r\n")) {
		ending = "\r\n"
	}
	hadTrailingNewline := len(original) == 0 || bytes.HasSuffix(original, []byte("\n"))

	var out strings.Builder
	out.Grow(len(original) + len(imports)*240)
	out.Write(original)
	if len(original) > 0 {
		if !hadTrailingNewline {
			out.WriteString(ending)
		}
		if !bytes.HasSuffix(original, []byte(ending+ending)) {
			out.WriteString(ending)
		}
	}

	blocks := make([]string, 0, len(imports))
	for _, imp := range imports {
		blocks = append(blocks, formatHostImport(imp))
	}
	body := strings.Join(blocks, "\n\n")
	if ending != "\n" {
		body = strings.ReplaceAll(body, "\n", ending)
	}
	out.WriteString(body)
	if hadTrailingNewline {
		out.WriteString(ending)
	}
	return []byte(out.String())
}

func formatHostImport(imp HostImport) string {
	var lines []string
	if comment := strings.TrimSpace(imp.Comment); comment != "" {
		if strings.HasPrefix(comment, "#") {
			lines = append(lines, comment)
		} else {
			lines = append(lines, "# "+comment)
		}
	}
	lines = append(lines, "Host "+imp.Alias)
	lines = append(lines, "  HostName "+quoteSSHValue(imp.HostName))
	if imp.User != "" {
		lines = append(lines, "  User "+quoteSSHValue(imp.User))
	}
	if imp.Port != 0 && imp.Port != 22 {
		lines = append(lines, "  Port "+strconv.Itoa(imp.Port))
	}
	if imp.IdentityFile != "" {
		lines = append(lines, "  IdentityFile "+quoteSSHValue(imp.IdentityFile))
	}
	if imp.ServerAliveInterval > 0 {
		lines = append(lines, "  ServerAliveInterval "+strconv.Itoa(imp.ServerAliveInterval))
	}
	if imp.Compression {
		lines = append(lines, "  Compression yes")
	}
	if imp.LegacySSHRSA {
		lines = append(lines, "  HostKeyAlgorithms +ssh-rsa", "  PubkeyAcceptedAlgorithms +ssh-rsa")
	}
	return strings.Join(lines, "\n")
}

func quoteSSHValue(value string) string {
	if value == "" || !strings.ContainsAny(value, " \t\"#\\") {
		return value
	}
	var out strings.Builder
	out.Grow(len(value) + 2)
	out.WriteByte('"')
	for _, r := range value {
		if r == '\\' || r == '"' {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	out.WriteByte('"')
	return out.String()
}

func writeImportBackup(target string, data []byte, mode fs.FileMode) (string, error) {
	stamp := time.Now().Format("20060102-150405.000000000")
	for attempt := 0; attempt < 100; attempt++ {
		suffix := stamp
		if attempt > 0 {
			suffix += "-" + strconv.Itoa(attempt)
		}
		backup := target + ".bak." + suffix
		file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("sshconfig: create backup %s: %w", backup, err)
		}
		if err := file.Chmod(mode); err != nil {
			_ = file.Close()
			_ = os.Remove(backup)
			return "", fmt.Errorf("sshconfig: chmod backup %s: %w", backup, err)
		}
		writeErr := writeSyncClose(file, data)
		if writeErr != nil {
			_ = os.Remove(backup)
			return "", fmt.Errorf("sshconfig: write backup %s: %w", backup, writeErr)
		}
		return backup, nil
	}
	return "", errors.New("sshconfig: could not allocate a unique backup name")
}

func replaceImportTarget(target string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".whop-import-*")
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
	if err := writeSyncClose(tmp, data); err != nil {
		cleanup()
		return fmt.Errorf("sshconfig: write temporary file: %w", err)
	}
	if err := atomic.ReplaceFile(tmpName, target); err != nil {
		cleanup()
		return fmt.Errorf("sshconfig: replace %s: %w", target, err)
	}
	return nil
}

func writeSyncClose(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
