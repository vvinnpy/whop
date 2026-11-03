// Package sshconfig discovers connectable hosts from OpenSSH client config.
package sshconfig

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// Kind describes the intended role of an SSH config entry.
type Kind string

const (
	KindServer  Kind = "server"
	KindGitAuth Kind = "git-auth"
)

// Host is a single concrete SSH alias suitable for a connection list.
type Host struct {
	Alias         string
	HostName      string
	User          string
	Port          int
	IdentityFiles []string
	ProxyJump     string
	Kind          Kind
	Tags          []string
	Source        string
}

// IsServer reports whether the host should appear in the default server list.
func (h Host) IsServer() bool {
	return h.Kind == KindServer
}

// Address returns host:port for direct dialing.
func (h Host) Address() string {
	host := h.HostName
	if host == "" {
		host = h.Alias
	}
	return fmt.Sprintf("%s:%d", host, h.Port)
}

// Load reads path, expands Include directives, and returns concrete aliases.
// A missing config file is not an error.
func Load(path string) ([]Host, error) {
	if path == "" {
		return nil, errors.New("sshconfig: empty config path")
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sshconfig: stat %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("sshconfig: %s is a directory", path)
	}

	expanded, err := expandIncludes(path, 0, map[string]bool{})
	if err != nil {
		return nil, err
	}

	cfg, err := ssh_config.Decode(strings.NewReader(expanded))
	if err != nil {
		return nil, fmt.Errorf("sshconfig: parse %s: %w", path, err)
	}

	seen := make(map[string]bool)
	var hosts []Host
	for _, block := range cfg.Hosts {
		if block == nil || isMatchBlock(block) {
			continue
		}

		for _, pattern := range block.Patterns {
			alias := cleanPattern(pattern.String())
			if alias == "" || isWildcardPattern(alias) {
				continue
			}
			if seen[alias] {
				continue
			}

			host, err := resolveHost(cfg, alias, block, path)
			if err != nil {
				return nil, err
			}
			seen[alias] = true
			hosts = append(hosts, host)
		}
	}
	return hosts, nil
}

// Servers returns only entries intended for the default host list.
func Servers(hosts []Host) []Host {
	out := make([]Host, 0, len(hosts))
	for _, host := range hosts {
		if host.IsServer() {
			out = append(out, host)
		}
	}
	return out
}

// Find returns the host with the exact alias.
func Find(hosts []Host, alias string) (Host, bool) {
	for _, host := range hosts {
		if host.Alias == alias {
			return host, true
		}
	}
	return Host{}, false
}

func resolveHost(cfg *ssh_config.Config, alias string, block *ssh_config.Host, source string) (Host, error) {
	hostName, err := cfg.Get(alias, "HostName")
	if err != nil {
		return Host{}, fmt.Errorf("sshconfig: resolve HostName for %s: %w", alias, err)
	}
	user, err := cfg.Get(alias, "User")
	if err != nil {
		return Host{}, fmt.Errorf("sshconfig: resolve User for %s: %w", alias, err)
	}
	portValue, err := cfg.Get(alias, "Port")
	if err != nil {
		return Host{}, fmt.Errorf("sshconfig: resolve Port for %s: %w", alias, err)
	}
	identityFiles, err := cfg.GetAll(alias, "IdentityFile")
	if err != nil {
		return Host{}, fmt.Errorf("sshconfig: resolve IdentityFile for %s: %w", alias, err)
	}
	proxyJump, err := cfg.Get(alias, "ProxyJump")
	if err != nil {
		return Host{}, fmt.Errorf("sshconfig: resolve ProxyJump for %s: %w", alias, err)
	}

	port := 22
	if portValue != "" {
		parsed, err := strconv.Atoi(portValue)
		if err != nil || parsed < 1 || parsed > 65535 {
			return Host{}, fmt.Errorf("sshconfig: invalid Port %q for %s", portValue, alias)
		}
		port = parsed
	}
	if hostName == "" {
		hostName = alias
	}

	return Host{
		Alias:         alias,
		HostName:      hostName,
		User:          user,
		Port:          port,
		IdentityFiles: identityFiles,
		ProxyJump:     proxyJump,
		Kind:          classify(alias, hostName, user),
		Tags:          parseTags(block),
		Source:        source,
	}, nil
}

func isMatchBlock(block *ssh_config.Host) bool {
	return strings.HasPrefix(strings.TrimSpace(block.String()), "Match ")
}

func isWildcardPattern(pattern string) bool {
	return strings.ContainsAny(pattern, "*?![")
}

func cleanPattern(pattern string) string {
	return strings.Trim(strings.TrimSpace(pattern), `"`)
}

func knownGitHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	known := []string{
		"github.com",
		"gitlab.com",
		"bitbucket.org",
		"codeberg.org",
		"ssh.dev.azure.com",
		"gitee.com",
		"git.sr.ht",
	}
	for _, candidate := range known {
		if host == candidate || strings.HasSuffix(host, "."+candidate) {
			return true
		}
	}
	return false
}

func classify(alias, hostName, user string) Kind {
	if knownGitHost(alias) || knownGitHost(hostName) || strings.EqualFold(user, "git") {
		return KindGitAuth
	}
	return KindServer
}

func parseTags(block *ssh_config.Host) []string {
	var values []string
	if block.EOLComment != "" {
		values = append(values, block.EOLComment)
	}
	for _, node := range block.Nodes {
		if empty, ok := node.(*ssh_config.Empty); ok && empty.Comment != "" {
			values = append(values, empty.Comment)
		}
	}

	seen := make(map[string]bool)
	var tags []string
	for _, value := range values {
		for _, tag := range extractTags(value) {
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	sort.Strings(tags)
	return tags
}

func extractTags(comment string) []string {
	const marker = "ttydeck:tags="
	index := strings.Index(strings.ToLower(comment), marker)
	if index < 0 {
		return nil
	}
	raw := comment[index+len(marker):]
	if fields := strings.Fields(raw); len(fields) > 0 {
		raw = fields[0]
	}
	var tags []string
	for _, tag := range strings.Split(raw, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func expandIncludes(path string, depth int, stack map[string]bool) (string, error) {
	if depth > 5 {
		return "", fmt.Errorf("sshconfig: Include depth exceeded at %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("sshconfig: resolve %s: %w", path, err)
	}
	if stack[abs] {
		return "", fmt.Errorf("sshconfig: recursive Include at %s", path)
	}
	stack[abs] = true
	defer delete(stack, abs)

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("sshconfig: open %s: %w", path, err)
	}
	defer file.Close()

	var out strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !isIncludeLine(line) {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}

		patterns, err := includePatterns(line)
		if err != nil {
			return "", fmt.Errorf("sshconfig: %s: %w", path, err)
		}
		for _, pattern := range patterns {
			matches, err := expandIncludePattern(pattern, filepath.Dir(path))
			if err != nil {
				return "", fmt.Errorf("sshconfig: %s: %w", path, err)
			}
			for _, match := range matches {
				content, err := expandIncludes(match, depth+1, stack)
				if err != nil {
					return "", err
				}
				out.WriteString(content)
				if !strings.HasSuffix(content, "\n") {
					out.WriteByte('\n')
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("sshconfig: read %s: %w", path, err)
	}
	return out.String(), nil
}

func isIncludeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return lower == "include" || strings.HasPrefix(lower, "include ") || strings.HasPrefix(lower, "include=")
}

func includePatterns(line string) ([]string, error) {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "include") {
		return nil, errors.New("not an Include line")
	}
	rest := strings.TrimSpace(trimmed[len("include"):])
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "="))
	if rest == "" {
		return nil, errors.New("Include requires a path")
	}
	return strings.Fields(rest), nil
}

func expandIncludePattern(pattern, baseDir string) ([]string, error) {
	pattern = expandHome(pattern)
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(baseDir, pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid Include pattern %q: %w", pattern, err)
	}
	sort.Strings(matches)
	return matches, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
