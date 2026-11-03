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

func TestLoadDiscoversConcreteServers(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	writeFile(t, configPath, `
Host *
  ServerAliveInterval 60

Host prod-db # ttydeck:tags=prod,db
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

Host *.internal
  User admin

Match host *.example.com
  User matchuser

Host tagged
  # ttydeck:tags=dev,test
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
