package sshconn

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func defaultHostKeyCallback() ssh.HostKeyCallback {
	home, err := os.UserHomeDir()
	if err != nil {
		return func(string, net.Addr, ssh.PublicKey) error {
			return fmt.Errorf("resolve home directory for known_hosts: %w", err)
		}
	}
	path := filepath.Join(home, ".ssh", "known_hosts")

	callback, err := knownhosts.New(path)
	if err == nil {
		return callback
	}

	filtered, filterErr := filterKnownHosts(path)
	if filterErr == nil && len(filtered) > 0 {
		tmp, tmpErr := os.CreateTemp("", "ttydeck-known-hosts-*")
		if tmpErr == nil {
			tmpName := tmp.Name()
			_, writeErr := tmp.Write(filtered)
			closeErr := tmp.Close()
			if writeErr == nil && closeErr == nil {
				filteredCallback, filteredErr := knownhosts.New(tmpName)
				_ = os.Remove(tmpName)
				if filteredErr == nil {
					return filteredCallback
				}
			} else {
				_ = os.Remove(tmpName)
			}
		}
	}

	return func(string, net.Addr, ssh.PublicKey) error {
		return fmt.Errorf("load known_hosts %s: %w", path, err)
	}
}

// filterKnownHosts drops entries whose public-key algorithm is not supported
// by the current x/crypto/ssh version. OpenSSH can write newer algorithms
// (for example ssh-mldsa44-ed25519) that older Go SSH parsers cannot decode.
func filterKnownHosts(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		keyIndex := 2
		if strings.HasPrefix(fields[0], "@") {
			keyIndex = 3
		}
		if len(fields) <= keyIndex {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(fields[keyIndex])
		if err != nil {
			continue
		}
		if _, err := ssh.ParsePublicKey(decoded); err != nil {
			continue
		}
		out.WriteString(trimmed)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
