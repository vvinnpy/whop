package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// resolvedTempDir returns a temporary directory with symlinks resolved so
// assertions match the canonical paths used by config editing. On macOS,
// t.TempDir is commonly reached through the /var -> /private/var symlink.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadDiscoversConcreteServers(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	writeFile(t, configPath, `
Host *
  ServerAliveInterval 60

Host prod-db # whop:tags=prod,db
  HostName 10.0.0.11
  User deploy
  Port 2222
  IdentityFile ~/.ssh/id_ed25519
  ProxyJump bastion

Host github.com
  User git
  IdentityFile ~/.ssh/id_github

Host gitlab.com
  User git

Host aur.archlinux.org
  User aur

Host *.internal
  User admin

Match host *.example.com
  User matchuser

Host tagged
  # whop:tags=dev,test
  HostName tagged.example.com

Include config.d/*.conf
`)
	writeFile(t, filepath.Join(dir, "config.d", "extra.conf"), `
Host staging-cache
  HostName 10.0.0.12
  User deploy
`)

	hosts, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}

	byAlias := map[string]Host{}
	for _, host := range hosts {
		byAlias[host.Alias] = host
	}

	if _, ok := byAlias["*"]; ok {
		t.Fatal("Host * must not be exposed")
	}
	if _, ok := byAlias["*.internal"]; ok {
		t.Fatal("wildcard host must not be exposed")
	}
	if _, ok := byAlias["*.example.com"]; ok {
		t.Fatal("Match host must not be exposed")
	}

	prod, ok := byAlias["prod-db"]
	if !ok {
		t.Fatal("prod-db missing")
	}
	if prod.HostName != "10.0.0.11" || prod.User != "deploy" || prod.Port != 2222 {
		t.Fatalf("unexpected prod-db: %+v", prod)
	}
	if prod.ProxyJump != "bastion" {
		t.Fatalf("unexpected proxy jump: %q", prod.ProxyJump)
	}
	if len(prod.IdentityFiles) != 1 || prod.IdentityFiles[0] != "~/.ssh/id_ed25519" {
		t.Fatalf("unexpected identity files: %#v", prod.IdentityFiles)
	}
	if prod.Kind != KindServer {
		t.Fatalf("prod-db kind = %q", prod.Kind)
	}
	if got := prod.Tags; len(got) != 2 || got[0] != "db" || got[1] != "prod" {
		t.Fatalf("unexpected tags: %#v", got)
	}

	if byAlias["github.com"].Kind != KindGitAuth {
		t.Fatalf("github.com kind = %q", byAlias["github.com"].Kind)
	}
	if byAlias["gitlab.com"].Kind != KindGitAuth {
		t.Fatalf("gitlab.com kind = %q", byAlias["gitlab.com"].Kind)
	}
	if byAlias["aur.archlinux.org"].Kind != KindGitAuth {
		t.Fatalf("aur.archlinux.org kind = %q", byAlias["aur.archlinux.org"].Kind)
	}

	tagged, ok := byAlias["tagged"]
	if !ok {
		t.Fatal("tagged host missing")
	}
	if got := tagged.Tags; len(got) != 2 || got[0] != "dev" || got[1] != "test" {
		t.Fatalf("unexpected tagged tags: %#v", got)
	}

	staging, ok := byAlias["staging-cache"]
	if !ok {
		t.Fatal("included host missing")
	}
	if staging.HostName != "10.0.0.12" {
		t.Fatalf("unexpected included host: %+v", staging)
	}

	servers := Servers(hosts)
	for _, host := range servers {
		if host.Kind != KindServer {
			t.Fatalf("Servers returned non-server: %+v", host)
		}
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	hosts, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 0 {
		t.Fatalf("expected no hosts, got %#v", hosts)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host broken\n  HostName 10.0.0.1\n  Port nope\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestFind(t *testing.T) {
	hosts := []Host{{Alias: "a"}, {Alias: "b"}}
	if host, ok := Find(hosts, "b"); !ok || host.Alias != "b" {
		t.Fatalf("unexpected find result: %+v %v", host, ok)
	}
	if _, ok := Find(hosts, "missing"); ok {
		t.Fatal("unexpected match")
	}
}

func TestLoadInheritsDefaultIdentityFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, `Host *
  IdentityFile ~/.ssh/id_ed25519

Host new-host
  HostName 10.0.0.9
`)
	hosts, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := Find(hosts, "new-host")
	if !ok {
		t.Fatal("new-host missing")
	}
	if len(host.IdentityFiles) != 1 || host.IdentityFiles[0] != "~/.ssh/id_ed25519" {
		t.Fatalf("default identity not inherited: %#v", host.IdentityFiles)
	}
}

func TestAccessModeOverridesInferredKind(t *testing.T) {
	login := Host{Alias: "git-login", Kind: KindGitAuth, AccessMode: AccessLogin}
	service := Host{Alias: "db-service", Kind: KindServer, AccessMode: AccessService}
	autoGit := Host{Alias: "github.com", Kind: KindGitAuth, AccessMode: AccessAuto}
	autoServer := Host{Alias: "prod", Kind: KindServer, AccessMode: AccessAuto}

	if !login.IsLoginable() || login.EffectiveAccess() != AccessLogin {
		t.Fatalf("manual login override not applied: %+v", login)
	}
	if service.IsLoginable() || service.EffectiveAccess() != AccessService {
		t.Fatalf("manual service override not applied: %+v", service)
	}
	if autoGit.IsLoginable() || autoGit.EffectiveAccess() != AccessService {
		t.Fatalf("auto git host should be service-only: %+v", autoGit)
	}
	if !autoServer.IsLoginable() || autoServer.EffectiveAccess() != AccessLogin {
		t.Fatalf("auto server should be loginable: %+v", autoServer)
	}

	servers := Servers([]Host{login, service, autoGit, autoServer})
	if len(servers) != 2 || servers[0].Alias != "git-login" || servers[1].Alias != "prod" {
		t.Fatalf("unexpected effective servers: %#v", servers)
	}
}

func TestParseAccessMode(t *testing.T) {
	tests := map[string]AccessMode{
		"auto":      AccessAuto,
		"automatic": AccessAuto,
		"login":     AccessLogin,
		"yes":       AccessLogin,
		"service":   AccessService,
		"no":        AccessService,
	}
	for input, want := range tests {
		got, ok := ParseAccessMode(input)
		if !ok || got != want {
			t.Fatalf("ParseAccessMode(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	if _, ok := ParseAccessMode("maybe"); ok {
		t.Fatal("expected invalid access mode to fail")
	}
}

func TestAccessModeFromOverride(t *testing.T) {
	if got := AccessModeFromOverride(nil); got != AccessAuto {
		t.Fatalf("nil override = %q", got)
	}
	yes, no := true, false
	if got := AccessModeFromOverride(&yes); got != AccessLogin {
		t.Fatalf("true override = %q", got)
	}
	if got := AccessModeFromOverride(&no); got != AccessService {
		t.Fatalf("false override = %q", got)
	}
}
