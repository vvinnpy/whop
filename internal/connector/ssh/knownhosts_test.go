package sshconn

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKnownHostLine(t *testing.T, hostname string) (string, ssh.PublicKey) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	line := hostname + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	return line, key
}

func TestFilterKnownHostsSkipsUnsupportedAlgorithms(t *testing.T) {
	valid, _ := testKnownHostLine(t, "[127.0.0.1]:2222")
	path := filepath.Join(t.TempDir(), "known_hosts")
	content := "# comment\n\n" + valid + "\n" +
		"[127.0.0.1]:2222 ssh-mldsa44-ed25519 AAAA\n" +
		"malformed line\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	filtered, err := filterKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(filtered), "ssh-ed25519") {
		t.Fatalf("valid key missing: %q", filtered)
	}
	if strings.Contains(string(filtered), "ssh-mldsa44-ed25519") {
		t.Fatalf("unsupported key retained: %q", filtered)
	}
	if strings.Contains(string(filtered), "malformed") {
		t.Fatalf("malformed line retained: %q", filtered)
	}
}

func TestDefaultHostKeyCallbackFiltersUnsupportedKnownHostEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	valid, key := testKnownHostLine(t, "[127.0.0.1]:2222")
	content := valid + "\n[127.0.0.1]:2222 ssh-mldsa44-ed25519 AAAA\n"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	callback := defaultHostKeyCallback()
	remote := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	if err := callback("[127.0.0.1]:2222", remote, key); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	other := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2223}
	if err := callback("[127.0.0.1]:2223", other, key); err == nil {
		t.Fatal("unknown host accepted")
	}
}
