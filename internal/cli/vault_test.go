package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"whop/internal/secrets"
	"whop/internal/sshconfig"
)

func TestVaultLifecycle(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "secrets.vault")
	t.Setenv("WHOP_VAULT_PATH", vaultPath)
	t.Setenv("WHOP_VAULT_PASSWORD", "master-secret")
	t.Setenv("WHOP_HOST_PASSWORD", "host-secret")

	td := newTestDeps()
	if code := Run([]string{"vault", "init"}, td.deps); code != 0 {
		t.Fatalf("init exit = %d: %s", code, td.stderr.String())
	}
	info, err := os.Stat(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("vault mode = %o", info.Mode().Perm())
	}
	if strings.Contains(td.stdout.String()+td.stderr.String(), "master-secret") || strings.Contains(td.stdout.String()+td.stderr.String(), "host-secret") {
		t.Fatalf("vault command leaked a secret: stdout=%q stderr=%q", td.stdout.String(), td.stderr.String())
	}

	td.stdout.Reset()
	if code := Run([]string{"vault", "set", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("set exit = %d: %s", code, td.stderr.String())
	}
	if strings.Contains(td.stdout.String()+td.stderr.String(), "host-secret") {
		t.Fatalf("vault set leaked host password")
	}
	vault, err := secrets.Open(vaultPath, "master-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := vault.Get("prod-db"); err != nil || !ok || got != "host-secret" {
		t.Fatalf("stored secret = %q, %v, %v", got, ok, err)
	}
	_ = vault.Close()

	td.stdout.Reset()
	if code := Run([]string{"vault", "list", "-json"}, td.deps); code != 0 {
		t.Fatalf("list exit = %d: %s", code, td.stderr.String())
	}
	var names []string
	if err := json.Unmarshal(td.stdout.Bytes(), &names); err != nil {
		t.Fatalf("invalid list JSON: %v\n%s", err, td.stdout.String())
	}
	if len(names) != 1 || names[0] != "prod-db" {
		t.Fatalf("names = %#v", names)
	}

	td.stdout.Reset()
	if code := Run([]string{"vault", "delete", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("delete exit = %d: %s", code, td.stderr.String())
	}
	vault, err = secrets.Open(vaultPath, "master-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := vault.Get("prod-db"); err != nil || ok {
		t.Fatalf("secret survived delete: ok=%v err=%v", ok, err)
	}
	_ = vault.Close()
}

func TestStoredVaultPasswordProvider(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "secrets.vault")
	vault, err := secrets.CreateWithParams(vaultPath, "master-secret", secrets.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Set("prod-db", "stored-host-secret"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Save(); err != nil {
		t.Fatal(err)
	}
	_ = vault.Close()
	t.Setenv("WHOP_VAULT_PATH", vaultPath)
	t.Setenv("WHOP_VAULT_PASSWORD", "master-secret")

	provider := defaultPasswordProvider()
	got, err := provider.Password(sshconfig.Host{Alias: "prod-db"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "stored-host-secret" {
		t.Fatalf("stored password = %q", got)
	}
}

func TestVaultWrongMasterPassword(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "secrets.vault")
	vault, err := secrets.CreateWithParams(vaultPath, "correct", secrets.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = vault.Close()
	t.Setenv("WHOP_VAULT_PATH", vaultPath)
	t.Setenv("WHOP_VAULT_PASSWORD", "wrong")
	td := newTestDeps()
	if code := Run([]string{"vault", "list"}, td.deps); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "invalid master password") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestVaultAutoUnlock(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "secrets.vault")
	keyPath := filepath.Join(dir, "secrets.key")
	t.Setenv("WHOP_VAULT_PATH", vaultPath)
	t.Setenv("WHOP_VAULT_KEY_FILE", keyPath)
	t.Setenv("WHOP_VAULT_PASSWORD", "")
	t.Setenv("WHOP_HOST_PASSWORD", "auto-host-secret")

	td := newTestDeps()
	if code := Run([]string{"vault", "init", "--auto-unlock"}, td.deps); code != 0 {
		t.Fatalf("init exit = %d: %s", code, td.stderr.String())
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", info.Mode().Perm())
	}
	if code := Run([]string{"vault", "set", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("set exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"vault", "status", "-json"}, td.deps); code != 0 {
		t.Fatalf("status exit = %d: %s", code, td.stderr.String())
	}
	var status vaultStatus
	if err := json.Unmarshal(td.stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Exists || !status.AutoUnlock {
		t.Fatalf("status = %+v", status)
	}

	provider := defaultPasswordProvider()
	got, err := provider.Password(sshconfig.Host{Alias: "prod-db"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "auto-host-secret" {
		t.Fatalf("password = %q", got)
	}
}

func TestTUIEditPasswordStoresInVault(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "secrets.vault")
	vault, err := secrets.CreateWithParams(vaultPath, "master-secret", secrets.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = vault.Close()
	t.Setenv("WHOP_VAULT_PATH", vaultPath)
	t.Setenv("WHOP_VAULT_KEY_FILE", filepath.Join(dir, "secrets.key"))
	t.Setenv("WHOP_VAULT_PASSWORD", "master-secret")

	td := newTestDeps()
	if code := Run(nil, td.deps); code != 0 {
		t.Fatalf("TUI exit = %d: %s", code, td.stderr.String())
	}
	if td.tuiOpts.SetHostPassword == nil {
		t.Fatal("TUI edit password callback is not configured")
	}
	if err := td.tuiOpts.SetHostPassword(sshconfig.Host{Alias: "prod-db"}, "new-host-secret"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(td.stdout.String()+td.stderr.String(), "new-host-secret") {
		t.Fatal("TUI password edit leaked the secret")
	}

	vault, err = secrets.Open(vaultPath, "master-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if got, ok, err := vault.Get("prod-db"); err != nil || !ok || got != "new-host-secret" {
		t.Fatalf("stored password = %q ok=%v err=%v", got, ok, err)
	}
}

func TestRenameVaultPassword(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "secrets.vault")
	vault, err := secrets.CreateWithParams(vaultPath, "master-secret", secrets.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Set("old-name", "host-secret"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Save(); err != nil {
		t.Fatal(err)
	}
	_ = vault.Close()
	t.Setenv("WHOP_VAULT_PATH", vaultPath)
	t.Setenv("WHOP_VAULT_KEY_FILE", filepath.Join(dir, "secrets.key"))
	t.Setenv("WHOP_VAULT_PASSWORD", "master-secret")

	if err := renameVaultPassword(newTestDeps().deps, "old-name", "new-name"); err != nil {
		t.Fatal(err)
	}
	vault, err = secrets.Open(vaultPath, "master-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if _, ok, err := vault.Get("old-name"); err != nil || ok {
		t.Fatalf("old password remains: ok=%v err=%v", ok, err)
	}
	if got, ok, err := vault.Get("new-name"); err != nil || !ok || got != "host-secret" {
		t.Fatalf("new password = %q ok=%v err=%v", got, ok, err)
	}
}
