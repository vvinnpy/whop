package sshconn

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"

	"ttydeck/internal/sshconfig"
)

type storedTestPasswordProvider struct {
	password string
	err      error
	calls    int
}

func (p *storedTestPasswordProvider) Password(sshconfig.Host) (string, error) {
	p.calls++
	return p.password, p.err
}

type storedTestKeyboardProvider struct {
	answers []string
	calls   int
}

func (p *storedTestKeyboardProvider) Password(sshconfig.Host) (string, error) {
	return "fallback-password", nil
}

func (p *storedTestKeyboardProvider) KeyboardInteractive(sshconfig.Host, string, string, []string, []bool) ([]string, error) {
	p.calls++
	return append([]string(nil), p.answers...), nil
}

func TestStoredPasswordProviderPrefersSecret(t *testing.T) {
	fallback := &storedTestPasswordProvider{password: "interactive"}
	provider := &StoredPasswordProvider{
		Lookup: func(sshconfig.Host) (string, bool, error) {
			return "stored", true, nil
		},
		Fallback: fallback,
	}
	got, err := provider.Password(sshconfig.Host{Alias: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "stored" || fallback.calls != 0 {
		t.Fatalf("password=%q fallback calls=%d", got, fallback.calls)
	}
}

func TestStoredPasswordProviderFallsBack(t *testing.T) {
	fallback := &storedTestPasswordProvider{password: "interactive"}
	provider := &StoredPasswordProvider{
		Lookup:   func(sshconfig.Host) (string, bool, error) { return "", false, nil },
		Fallback: fallback,
	}
	got, err := provider.Password(sshconfig.Host{Alias: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "interactive" || fallback.calls != 1 {
		t.Fatalf("password=%q fallback calls=%d", got, fallback.calls)
	}
}

func TestStoredPasswordProviderReturnsLookupError(t *testing.T) {
	fallback := &storedTestPasswordProvider{password: "interactive"}
	want := errors.New("bad master password")
	provider := &StoredPasswordProvider{
		Lookup:   func(sshconfig.Host) (string, bool, error) { return "", false, want },
		Fallback: fallback,
	}
	_, err := provider.Password(sshconfig.Host{Alias: "prod"})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if fallback.calls != 0 {
		t.Fatal("fallback should not run after lookup error")
	}
}

func TestStoredPasswordProviderKeyboardInteractive(t *testing.T) {
	fallback := &storedTestKeyboardProvider{answers: []string{"fallback"}}
	provider := &StoredPasswordProvider{
		Lookup: func(sshconfig.Host) (string, bool, error) {
			return "stored-secret", true, nil
		},
		Fallback: fallback,
	}
	answers, err := provider.KeyboardInteractive(sshconfig.Host{}, "password", "", []string{"Password:"}, []bool{false})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(answers, []string{"stored-secret"}) || fallback.calls != 0 {
		t.Fatalf("answers=%#v fallback calls=%d", answers, fallback.calls)
	}

	answers, err = provider.KeyboardInteractive(sshconfig.Host{}, "otp", "", []string{"Password:", "OTP:"}, []bool{false, false})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(answers, []string{"fallback"}) || fallback.calls != 1 {
		t.Fatalf("multi-question answers=%#v fallback calls=%d", answers, fallback.calls)
	}
}

func TestStoredPasswordProviderErrorsAreUseful(t *testing.T) {
	provider := &StoredPasswordProvider{}
	_, err := provider.Password(sshconfig.Host{})
	if err == nil || !strings.Contains(err.Error(), "no password provider") {
		t.Fatalf("error = %v", err)
	}
	_, err = provider.KeyboardInteractive(sshconfig.Host{}, "", "", []string{"x"}, []bool{false})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoredPasswordProviderPasswordAuth(t *testing.T) {
	server := newTestSSHServerWithPassword(t, "vault-secret")
	fallback := &storedTestPasswordProvider{password: "interactive"}
	provider := &StoredPasswordProvider{
		Lookup: func(sshconfig.Host) (string, bool, error) {
			return "vault-secret", true, nil
		},
		Fallback: fallback,
	}
	connector := New(Options{
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		DisableAgent:    true,
		Timeout:         5 * time.Second,
		Username:        "tester",
		Password:        provider,
	})
	host := sshconfig.Host{
		Alias:         "password-host",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{filepath.Join(t.TempDir(), "missing-key")},
	}
	output, err := connector.RunCommand(context.Background(), host, "echo stored-password-ok")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "ran: echo stored-password-ok") {
		t.Fatalf("output = %q", output)
	}
	if fallback.calls != 0 {
		t.Fatalf("interactive fallback called %d times", fallback.calls)
	}
}
