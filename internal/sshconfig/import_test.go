package sshconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAddHostsAddsMultipleBlocksAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "# global comment\nHost existing\n  HostName 10.0.0.1\n"
	writeFile(t, path, original)

	result, err := AddHosts(path, []HostImport{
		{
			Alias:               "prod-db",
			HostName:            "db.internal",
			User:                "deploy",
			Port:                2222,
			IdentityFile:        "~/.ssh/id_ed25519",
			ServerAliveInterval: 30,
			Compression:         true,
			LegacySSHRSA:        true,
			Comment:             "production database",
		},
		{
			Alias:    "staging",
			HostName: "staging.internal",
			Port:     22,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Added, []string{"prod-db", "staging"}; !equalStrings(got, want) {
		t.Fatalf("Added = %#v, want %#v", got, want)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("unexpected skipped aliases: %#v", result.Skipped)
	}
	if result.Backup == "" {
		t.Fatal("missing backup path")
	}

	updatedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(updatedBytes)
	if !strings.HasPrefix(updated, original) {
		t.Fatalf("existing content was not preserved:\n%s", updated)
	}
	for _, want := range []string{
		"# production database\nHost prod-db",
		"  HostName db.internal",
		"  User deploy",
		"  Port 2222",
		"  IdentityFile ~/.ssh/id_ed25519",
		"  ServerAliveInterval 30",
		"  Compression yes",
		"  HostKeyAlgorithms +ssh-rsa",
		"  PubkeyAcceptedAlgorithms +ssh-rsa",
		"Host staging",
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("missing %q in:\n%s", want, updated)
		}
	}
	staging := updated[strings.Index(updated, "Host staging"):]
	if strings.Contains(staging, "Port 22") {
		t.Fatalf("default port should be omitted:\n%s", staging)
	}

	hosts, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	prod, ok := Find(hosts, "prod-db")
	if !ok {
		t.Fatal("prod-db missing from parsed config")
	}
	if prod.HostName != "db.internal" || prod.User != "deploy" || prod.Port != 2222 {
		t.Fatalf("unexpected prod-db host: %+v", prod)
	}

	backup, err := os.ReadFile(result.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Fatal("backup does not contain the original config")
	}
}

func TestAddHostsSkipsRootAndIncludedAliasesWithoutWriting(t *testing.T) {
	dir := resolvedTempDir(t)
	root := filepath.Join(dir, "config")
	includeDir := filepath.Join(dir, "config.d")
	include := filepath.Join(includeDir, "work.conf")
	rootText := "Include config.d/*.conf\n\nHost root-host\n  HostName root.internal\n"
	includeText := "Host included-host\n  HostName included.internal\n"
	writeFile(t, root, rootText)
	writeFile(t, include, includeText)

	result, err := AddHosts(root, []HostImport{
		{Alias: "root-host", HostName: "new-root.internal", Port: 22},
		{Alias: "INCLUDED-HOST", HostName: "new-included.internal", Port: 22},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Added) != 0 {
		t.Fatalf("unexpected additions: %#v", result.Added)
	}
	if got, want := result.Skipped, []string{"root-host", "INCLUDED-HOST"}; !equalStrings(got, want) {
		t.Fatalf("Skipped = %#v, want %#v", got, want)
	}
	if result.Backup != "" {
		t.Fatalf("unexpected backup: %s", result.Backup)
	}

	assertFileEquals(t, root, rootText)
	assertFileEquals(t, include, includeText)
	assertNoImportArtifacts(t, dir, "config")
}

func TestAddHostsSkipsDuplicateInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host existing\n  HostName 10.0.0.1\n")

	result, err := AddHosts(path, []HostImport{
		{Alias: "duplicate", HostName: "dup.internal", Port: 22},
		{Alias: "DUPLICATE", HostName: "dup-2.internal", Port: 22},
		{Alias: "existing", HostName: "old.internal", Port: 22},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Added, []string{"duplicate"}; !equalStrings(got, want) {
		t.Fatalf("Added = %#v, want %#v", got, want)
	}
	if got, want := result.Skipped, []string{"DUPLICATE", "existing"}; !equalStrings(got, want) {
		t.Fatalf("Skipped = %#v, want %#v", got, want)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "Host duplicate") != 1 || strings.Contains(string(content), "dup-2.internal") {
		t.Fatalf("unexpected duplicate block:\n%s", content)
	}
}

func TestAddHostsInvalidInputDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host existing\n  HostName 10.0.0.1\n"
	writeFile(t, path, original)

	tests := []struct {
		name string
		imp  HostImport
	}{
		{name: "empty alias", imp: HostImport{HostName: "host.internal"}},
		{name: "alias newline", imp: HostImport{Alias: "bad\nHost injected", HostName: "host.internal"}},
		{name: "alias whitespace", imp: HostImport{Alias: "bad alias", HostName: "host.internal"}},
		{name: "alias wildcard", imp: HostImport{Alias: "bad*", HostName: "host.internal"}},
		{name: "empty hostname", imp: HostImport{Alias: "host"}},
		{name: "hostname newline", imp: HostImport{Alias: "host", HostName: "host.internal\n"}},
		{name: "hostname whitespace", imp: HostImport{Alias: "host", HostName: "bad host"}},
		{name: "zero port", imp: HostImport{Alias: "host", HostName: "host.internal"}},
		{name: "negative port", imp: HostImport{Alias: "host", HostName: "host.internal", Port: -1}},
		{name: "large port", imp: HostImport{Alias: "host", HostName: "host.internal", Port: 65536}},
		{name: "negative keepalive", imp: HostImport{Alias: "host", HostName: "host.internal", Port: 22, ServerAliveInterval: -1}},
		{name: "user control", imp: HostImport{Alias: "host", HostName: "host.internal", Port: 22, User: "bad\x00user"}},
		{name: "identity newline", imp: HostImport{Alias: "host", HostName: "host.internal", Port: 22, IdentityFile: "id\nfile"}},
		{name: "comment newline", imp: HostImport{Alias: "host", HostName: "host.internal", Port: 22, Comment: "comment\ninjected"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := AddHosts(path, []HostImport{test.imp})
			if err == nil {
				t.Fatal("expected validation error")
			}
			if len(result.Added) != 0 || result.Backup != "" {
				t.Fatalf("unexpected result: %+v", result)
			}
			assertFileEquals(t, path, original)
		})
	}
	assertNoImportArtifacts(t, filepath.Dir(path), "config")
}

func TestAddHostsPreservesSymlinkModeAndBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation and Unix mode assertions are not portable")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real-config")
	link := filepath.Join(dir, "config")
	original := "Host existing\n  HostName 10.0.0.1\n"
	writeFile(t, target, original)
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	result, err := AddHosts(link, []HostImport{{Alias: "linked-new", HostName: "new.internal", Port: 22}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("root config symlink was replaced")
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targetInfo.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("target mode = %o, want %o", got, want)
	}
	if !strings.HasPrefix(result.Backup, target+".bak.") {
		t.Fatalf("backup = %q, want target-side backup", result.Backup)
	}
	backupInfo, err := os.Stat(result.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := backupInfo.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("backup mode = %o, want %o", got, want)
	}
	assertFileEquals(t, result.Backup, original)
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "Host linked-new") {
		t.Fatalf("symlink target was not updated:\n%s", updated)
	}
}

func TestAddHostsPreservesTrailingNewlineState(t *testing.T) {
	tests := []struct {
		name         string
		original     string
		wantTrailing bool
	}{
		{name: "with newline", original: "Host existing\n  HostName 10.0.0.1\n", wantTrailing: true},
		{name: "without newline", original: "Host existing\n  HostName 10.0.0.1", wantTrailing: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			writeFile(t, path, test.original)
			if _, err := AddHosts(path, []HostImport{{Alias: "new", HostName: "new.internal", Port: 22}}); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(content), test.original) {
				t.Fatalf("original bytes changed:\n%s", content)
			}
			if got := strings.HasSuffix(string(content), "\n"); got != test.wantTrailing {
				t.Fatalf("trailing newline = %v, want %v in %q", got, test.wantTrailing, content)
			}
		})
	}
}

func TestAddHostsPreservesCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host existing\r\n  HostName 10.0.0.1\r\n"
	writeFile(t, path, original)
	if _, err := AddHosts(path, []HostImport{{Alias: "windows", HostName: "windows.internal", Port: 22}}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), original) {
		t.Fatalf("existing CRLF content changed:\n%q", content)
	}
	if !strings.HasSuffix(string(content), "\r\n") {
		t.Fatalf("CRLF final newline was not preserved: %q", content)
	}
	withoutCRLF := strings.ReplaceAll(string(content), "\r\n", "")
	if strings.Contains(withoutCRLF, "\n") {
		t.Fatalf("mixed newline styles in appended config: %q", content)
	}
}

func TestAddHostsCreatesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	result, err := AddHosts(path, []HostImport{{Alias: "fresh", HostName: "fresh.internal", Port: 22}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Added, []string{"fresh"}; !equalStrings(got, want) {
		t.Fatalf("Added = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("new config mode = %o, want 600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(content), "\n") {
		t.Fatalf("new config should end with newline: %q", content)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertFileEquals(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s changed:\ngot:  %q\nwant: %q", path, got, want)
	}
}

func assertNoImportArtifacts(t *testing.T, dir, base string) {
	t.Helper()
	for _, pattern := range []string{base + ".lock", base + ".bak.*", ".whop-import-*"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("unexpected artifacts for %q: %#v", pattern, matches)
		}
	}
}

func TestPreviewAndAddHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host existing\n  HostName 10.0.0.1\n")
	imp := HostImport{Alias: "new-host", HostName: "10.0.0.2", User: "deploy", Port: 2222, ProxyJump: "bastion"}
	preview, err := PreviewHostAdd(path, imp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Diff, "+Host new-host") || preview.Hash == "" {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if err := AddHost(path, imp, preview.Hash); err != nil {
		t.Fatal(err)
	}
	hosts, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := Find(hosts, "new-host")
	if !ok || host.HostName != "10.0.0.2" || host.User != "deploy" || host.Port != 2222 || host.ProxyJump != "bastion" {
		t.Fatalf("new host = %+v ok=%v", host, ok)
	}
	if err := AddHost(path, imp, ""); !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate add error = %v", err)
	}
}

func TestAddHostRejectsStalePreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host existing\n  HostName 10.0.0.1\n")
	preview, err := PreviewHostAdd(path, HostImport{Alias: "new", HostName: "10.0.0.2", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "Host changed\n  HostName 10.0.0.3\n")
	if err := AddHost(path, HostImport{Alias: "new", HostName: "10.0.0.2", Port: 22}, preview.Hash); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("stale add error = %v", err)
	}
}
