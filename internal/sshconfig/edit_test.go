package sshconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func strPtr(value string) *string { return &value }
func intPtr(value int) *int       { return &value }
func tagsPtr(values ...string) *[]string {
	return &values
}

func TestEditHostPreservesCommentsAndUpdatesFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := `# global comment
Host prod-db # keep this comment
  HostName 10.0.0.11
  User deploy
  # keep inner comment
  Port 22

Host staging
  HostName 10.0.0.12
`
	writeFile(t, path, original)

	if err := EditHost(path, "prod-db", Edit{
		HostName:  strPtr("10.0.0.99"),
		User:      strPtr("admin"),
		Port:      intPtr(2222),
		ProxyJump: strPtr("bastion"),
		Tags:      tagsPtr("prod", "db"),
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{"# global comment", "# keep this comment", "# keep inner comment", "Host staging"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "10.0.0.11") {
		t.Fatalf("old hostname remains:\n%s", text)
	}
	for _, want := range []string{"HostName 10.0.0.99", "User admin", "Port 2222", "ProxyJump bastion", "ttydeck:tags=prod,db"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}

	hosts, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := Find(hosts, "prod-db")
	if !ok {
		t.Fatal("prod-db missing after edit")
	}
	if host.HostName != "10.0.0.99" || host.User != "admin" || host.Port != 2222 || host.ProxyJump != "bastion" {
		t.Fatalf("unexpected edited host: %+v", host)
	}
	if len(host.Tags) != 2 || host.Tags[0] != "db" || host.Tags[1] != "prod" {
		t.Fatalf("unexpected tags: %#v", host.Tags)
	}

	staging, ok := Find(hosts, "staging")
	if !ok || staging.HostName != "10.0.0.12" {
		t.Fatalf("staging was changed: %+v", staging)
	}

	backups, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v", backups)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Fatal("backup content differs from original")
	}
}

func TestEditHostAddsMissingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host minimal\n  HostName 10.0.0.1\n")

	if err := EditHost(path, "minimal", Edit{
		User:      strPtr("root"),
		Port:      intPtr(2200),
		ProxyJump: strPtr("jump"),
		Tags:      tagsPtr("dev"),
	}); err != nil {
		t.Fatal(err)
	}

	hosts, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := Find(hosts, "minimal")
	if !ok {
		t.Fatal("minimal missing")
	}
	if host.User != "root" || host.Port != 2200 || host.ProxyJump != "jump" || len(host.Tags) != 1 || host.Tags[0] != "dev" {
		t.Fatalf("unexpected host: %+v", host)
	}
}

func TestEditHostReplacesIdentityFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, `Host multi-key
  HostName 10.0.0.1
  IdentityFile ~/.ssh/id_rsa
  IdentityFile ~/.ssh/id_ed25519
`)

	if err := EditHost(path, "multi-key", Edit{IdentityFile: strPtr("~/.ssh/id_new")}); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(path)
	if strings.Count(string(text), "IdentityFile") != 1 || !strings.Contains(string(text), "id_new") {
		t.Fatalf("unexpected identity files:\n%s", text)
	}
}

func TestEditHostUnknownAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host known\n  HostName 10.0.0.1\n")
	if err := EditHost(path, "missing", Edit{User: strPtr("root")}); err == nil {
		t.Fatal("expected error")
	}
}

func TestEditHostInvalidPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host known\n  HostName 10.0.0.1\n")
	if err := EditHost(path, "known", Edit{Port: intPtr(70000)}); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestEditHostPreservesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real-config")
	link := filepath.Join(dir, "config")
	writeFile(t, target, "Host linked\n  HostName 10.0.0.1\n")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := EditHost(link, "linked", Edit{HostName: strPtr("10.0.0.2")}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file")
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "HostName 10.0.0.2") {
		t.Fatalf("target not updated:\n%s", updated)
	}
}

func TestEditHostPreservesTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host newline\n  HostName 10.0.0.1\n")
	time.Sleep(time.Millisecond)
	if err := EditHost(path, "newline", Edit{User: strPtr("root")}); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(content), "\n") {
		t.Fatalf("trailing newline lost: %q", content)
	}
}

func TestEditHostInIncludedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("include path assertions below use Unix-style paths")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	includeDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(includeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	included := filepath.Join(includeDir, "work.conf")
	rootText := "Include config.d/*.conf\n"
	includedText := `Host prod-db
  HostName 10.0.0.11
  User deploy
`
	writeFile(t, root, rootText)
	writeFile(t, included, includedText)

	if err := EditHost(root, "prod-db", Edit{
		HostName: strPtr("10.0.0.99"),
		User:     strPtr("admin"),
	}); err != nil {
		t.Fatal(err)
	}

	rootAfter, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(rootAfter) != rootText {
		t.Fatalf("root config changed:\n%s", rootAfter)
	}
	includedAfter, err := os.ReadFile(included)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HostName 10.0.0.99", "User admin"} {
		if !strings.Contains(string(includedAfter), want) {
			t.Fatalf("missing %q in included config:\n%s", want, includedAfter)
		}
	}
	if strings.Contains(string(includedAfter), "10.0.0.11") {
		t.Fatalf("old included value remains:\n%s", includedAfter)
	}

	backups, err := filepath.Glob(included + ".bak.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one included-file backup, got %#v", backups)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != includedText {
		t.Fatal("included backup content differs from original")
	}
	if rootBackups, _ := filepath.Glob(root + ".bak.*"); len(rootBackups) != 0 {
		t.Fatalf("unexpected root backups: %#v", rootBackups)
	}
}

func TestEditHostChoosesOnlyMatchingInclude(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	includeDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(includeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(includeDir, "10-first.conf")
	second := filepath.Join(includeDir, "20-second.conf")
	writeFile(t, root, "Include config.d/*.conf\n")
	writeFile(t, first, "Host first\n  HostName 10.0.0.1\n")
	writeFile(t, second, "Host second\n  HostName 10.0.0.2\n")

	if err := EditHost(root, "second", Edit{User: strPtr("root")}); err != nil {
		t.Fatal(err)
	}
	firstAfter, _ := os.ReadFile(first)
	if strings.Contains(string(firstAfter), "User root") {
		t.Fatalf("wrong include changed:\n%s", firstAfter)
	}
	secondAfter, _ := os.ReadFile(second)
	if !strings.Contains(string(secondAfter), "User root") {
		t.Fatalf("matching include not changed:\n%s", secondAfter)
	}
}

func TestEditHostRecursiveIncludeReturnsError(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	child := filepath.Join(dir, "child.conf")
	if err := os.Symlink(root, child); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeFile(t, root, "Include child.conf\n")

	err := EditHost(root, "missing", Edit{User: strPtr("root")})
	if err == nil || !strings.Contains(err.Error(), "recursive Include") {
		t.Fatalf("expected recursive Include error, got %v", err)
	}
}

func TestEditHostPreservesIncludedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	includeDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(includeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(includeDir, "real.conf")
	link := filepath.Join(includeDir, "linked.conf")
	writeFile(t, root, "Include config.d/linked.conf\n")
	writeFile(t, target, "Host linked\n  HostName 10.0.0.1\n")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := EditHost(root, "linked", Edit{HostName: strPtr("10.0.0.2")}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("included symlink was replaced by a regular file")
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "HostName 10.0.0.2") {
		t.Fatalf("included symlink target not updated:\n%s", updated)
	}
}
