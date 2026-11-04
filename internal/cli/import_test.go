package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ttydeck/internal/secrets"
	"ttydeck/internal/sshconfig"
)

func TestImportNCCMDryRunAndApply(t *testing.T) {
	dir := t.TempDir()
	nccmPath := filepath.Join(dir, "nccm.yml")
	configPath := filepath.Join(dir, "ssh_config")
	original := `Host 232
  HostName 10.215.211.232
  User root
  IdentityFile /home/k/Tools/develop_wh.pem

Host existing
  HostName 10.0.0.1
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	nccmData := `nccm_config_keepalive: 30
10.215.211.232:
  address: 10.215.211.232
  user: root
  identity: /home/k/Tools/develop_wh.pem
  keepalive: 40
  customargs: -z ssh-rsa
lab:
  address: 10.20.0.5
  user: deploy
  port: 2222
  keepalive: 20
  customargs: -C
password-host:
  address: 10.20.0.6
  user: root
  customargs: -P 'SuperSecretValue' -z expect-ssh-rsa
`
	if err := os.WriteFile(nccmPath, []byte(nccmData), 0o600); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(dir, "secrets.vault")
	t.Setenv("TTYDECK_VAULT_PATH", vaultPath)
	t.Setenv("TTYDECK_VAULT_PASSWORD", "import-master")
	td := newTestDeps()
	td.deps.LoadConfig = sshconfig.Load
	td.deps.RecordingDir = dir

	if code := Run([]string{"import", "nccm", "-file", nccmPath, "-config", configPath}, td.deps); code != 0 {
		t.Fatalf("dry-run exit = %d: %s", code, td.stderr.String())
	}
	content, _ := os.ReadFile(configPath)
	if string(content) != original {
		t.Fatalf("dry-run changed config:\n%s", content)
	}
	out := td.stdout.String() + td.stderr.String()
	if !strings.Contains(out, "would add\tlab") || !strings.Contains(out, "would add\tpassword-host") {
		t.Fatalf("dry-run output = %q", out)
	}
	if !strings.Contains(out, "same endpoint as 232") {
		t.Fatalf("missing endpoint duplicate report: %q", out)
	}
	if strings.Contains(out, "SuperSecretValue") {
		t.Fatalf("password leaked in output: %q", out)
	}
	if _, err := os.Stat(vaultPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run created vault: %v", err)
	}

	td.stdout.Reset()
	td.stderr.Reset()
	if code := Run([]string{"import", "nccm", "-file", nccmPath, "-config", configPath, "-apply", "-json"}, td.deps); code != 0 {
		t.Fatalf("apply exit = %d: %s", code, td.stderr.String())
	}
	var report importReport
	if err := json.Unmarshal(td.stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, td.stdout.String())
	}
	if len(report.Added) != 2 || report.Backup == "" || report.PasswordsAvailable != 1 || len(report.PasswordsStored) != 1 {
		t.Fatalf("report = %+v", report)
	}
	content, _ = os.ReadFile(configPath)
	text := string(content)
	for _, want := range []string{
		"Host lab",
		"HostName 10.20.0.5",
		"Port 2222",
		"ServerAliveInterval 20",
		"Compression yes",
		"Host password-host",
		"HostKeyAlgorithms +ssh-rsa",
		"PubkeyAcceptedAlgorithms +ssh-rsa",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in imported config:\n%s", want, text)
		}
	}
	if strings.Contains(text, "SuperSecretValue") {
		t.Fatalf("password leaked into ssh config:\n%s", text)
	}
	vault, err := secrets.Open(vaultPath, "import-master")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if got, ok, err := vault.Get("password-host"); err != nil || !ok || got != "SuperSecretValue" {
		t.Fatalf("vault password = %q, %v, %v", got, ok, err)
	}
}

func TestImportNCCMOnlyAndJSON(t *testing.T) {
	dir := t.TempDir()
	nccmPath := filepath.Join(dir, "nccm.yml")
	configPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nccmPath, []byte("one:\n  address: 10.0.0.1\n  user: root\ntwo:\n  address: 10.0.0.2\n  user: root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.LoadConfig = sshconfig.Load
	if code := Run([]string{"import", "nccm", "-file", nccmPath, "-config", configPath, "-only", "two", "-json"}, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	var report importReport
	if err := json.Unmarshal(td.stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Added) != 1 || report.Added[0] != "two" {
		t.Fatalf("report = %+v", report)
	}
}

func TestImportNCCMNormalizesLeadingHashAlias(t *testing.T) {
	dir := t.TempDir()
	nccmPath := filepath.Join(dir, "nccm.yml")
	if err := os.WriteFile(nccmPath, []byte("\"#jumper\":\n  address: 10.0.0.9\n  user: root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.LoadConfig = sshconfig.Load
	if code := Run([]string{"import", "nccm", "-file", nccmPath, "-config", filepath.Join(dir, "config"), "-json"}, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	var report importReport
	if err := json.Unmarshal(td.stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Added) != 1 || report.Added[0] != "jumper" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Warnings) == 0 || !strings.Contains(report.Warnings[0], "alias renamed") {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
}
