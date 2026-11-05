package sshconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func previewStringPtr(value string) *string { return &value }
func previewIntPtr(value int) *int          { return &value }
func previewTagsPtr(values ...string) *[]string {
	return &values
}

func TestPreviewHostRootConfig(t *testing.T) {
	path := filepath.Join(resolvedTempDir(t), "config")
	original := `# global comment
Host prod-db
  HostName 10.0.0.11
  User deploy
  # keep inner comment
  Port 22
  ProxyJump old-jump
  # whop:tags=old

Host staging
  HostName 10.0.0.12
  User staging
`
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "prod-db", Edit{
		HostName:  previewStringPtr("10.0.0.99"),
		User:      previewStringPtr("admin"),
		Port:      previewIntPtr(2222),
		ProxyJump: previewStringPtr("bastion"),
		Tags:      previewTagsPtr("prod", "db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(diff, "--- "+path+"\n+++ "+path+"\n@@ ") {
		t.Fatalf("unexpected diff header:\n%s", diff)
	}
	for _, want := range []string{
		"-  HostName 10.0.0.11",
		"+  HostName 10.0.0.99",
		"-  User deploy",
		"+  User admin",
		"-  Port 22",
		"+  Port 2222",
		"-  ProxyJump old-jump",
		"+  ProxyJump bastion",
		"-  # whop:tags=old",
		"+  # whop:tags=prod,db",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("missing %q in diff:\n%s", want, diff)
		}
	}
	assertPreviewOnlyChangesHost(t, diff, "staging")
	assertPreviewReadOnly(t, path, original)
}

func TestPreviewHostInIncludedFile(t *testing.T) {
	dir := resolvedTempDir(t)
	root := filepath.Join(dir, "config")
	includeDir := filepath.Join(dir, "config.d")
	first := filepath.Join(includeDir, "10-first.conf")
	second := filepath.Join(includeDir, "20-second.conf")
	rootText := "Include config.d/*.conf\n"
	firstText := "Host first\n  HostName 10.0.0.1\n"
	secondText := "Host second\n  HostName 10.0.0.2\n  User old\n"
	writeFile(t, root, rootText)
	writeFile(t, first, firstText)
	writeFile(t, second, secondText)

	diff, err := PreviewHost(root, "second", Edit{User: previewStringPtr("root")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(diff, "--- "+second+"\n+++ "+second+"\n@@ ") {
		t.Fatalf("diff should target included file %s:\n%s", second, diff)
	}
	if !strings.Contains(diff, "-  User old\n+  User root\n") {
		t.Fatalf("missing included host change:\n%s", diff)
	}
	assertPreviewReadOnly(t, root, rootText)
	assertPreviewReadOnly(t, first, firstText)
	assertPreviewReadOnly(t, second, secondText)
}

func TestPreviewHostIdentityFileAddAndReplacement(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		original := "Host target\n  HostName 10.0.0.1\n"
		writeFile(t, path, original)

		diff, err := PreviewHost(path, "target", Edit{IdentityFile: previewStringPtr("~/.ssh/id_ed25519")})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diff, "+  IdentityFile ~/.ssh/id_ed25519\n") {
			t.Fatalf("missing added IdentityFile:\n%s", diff)
		}
		assertPreviewReadOnly(t, path, original)
	})

	t.Run("replace removes old entries", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		original := `Host target
  HostName 10.0.0.1
  IdentityFile ~/.ssh/id_rsa
  IdentityFile ~/.ssh/id_ed25519
`
		writeFile(t, path, original)

		diff, err := PreviewHost(path, "target", Edit{IdentityFile: previewStringPtr("~/.ssh/id_new")})
		if err != nil {
			t.Fatal(err)
		}
		for _, removed := range []string{"-  IdentityFile ~/.ssh/id_rsa\n", "-  IdentityFile ~/.ssh/id_ed25519\n"} {
			if !strings.Contains(diff, removed) {
				t.Fatalf("missing removed entry %q:\n%s", removed, diff)
			}
		}
		if !strings.Contains(diff, "+  IdentityFile ~/.ssh/id_new\n") {
			t.Fatalf("missing replacement IdentityFile:\n%s", diff)
		}
		assertPreviewReadOnly(t, path, original)
	})
}

func TestPreviewHostUpdatesTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host target\n  HostName 10.0.0.1\n  # whop:tags=old,dev\n"
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "target", Edit{Tags: previewTagsPtr("prod", "db")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-  # whop:tags=old,dev\n+  # whop:tags=prod,db\n") {
		t.Fatalf("missing tag change:\n%s", diff)
	}
	assertPreviewReadOnly(t, path, original)
}

func TestPreviewHostNoChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host target\n  HostName 10.0.0.1\n  User deploy\n"
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "target", Edit{
		HostName: previewStringPtr("10.0.0.1"),
		User:     previewStringPtr("deploy"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff != "" {
		t.Fatalf("expected empty diff, got:\n%s", diff)
	}
	assertPreviewReadOnly(t, path, original)
}

func TestPreviewHostMissingAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host known\n  HostName 10.0.0.1\n"
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "missing", Edit{User: previewStringPtr("root")})
	if !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("expected ErrHostNotFound, got diff=%q err=%v", diff, err)
	}
	if diff != "" {
		t.Fatalf("expected empty diff on error, got:\n%s", diff)
	}
	assertPreviewReadOnly(t, path, original)
}

func TestPreviewHostRecursiveIncludeReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	child := filepath.Join(dir, "child.conf")
	writeFile(t, root, "Include child.conf\n")
	if err := os.Symlink(root, child); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	diff, err := PreviewHost(root, "missing", Edit{User: previewStringPtr("root")})
	if err == nil || !strings.Contains(err.Error(), "recursive Include") {
		t.Fatalf("expected recursive Include error, got diff=%q err=%v", diff, err)
	}
	if diff != "" {
		t.Fatalf("expected empty diff on error, got:\n%s", diff)
	}
}

func TestPreviewHostMalformedIncludeReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Include [\nHost known\n  HostName 10.0.0.1\n"
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "missing", Edit{User: previewStringPtr("root")})
	if err == nil || !strings.Contains(err.Error(), "invalid Include pattern") {
		t.Fatalf("expected invalid Include pattern error, got diff=%q err=%v", diff, err)
	}
	if diff != "" {
		t.Fatalf("expected empty diff on error, got:\n%s", diff)
	}
	assertPreviewReadOnly(t, path, original)
}

func TestPreviewHostPreservesCRLFAndFinalNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host target\r\n  HostName 10.0.0.1\r\n  User old\r\n"
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "target", Edit{User: previewStringPtr("new")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(diff, '\r') {
		t.Fatalf("diff should contain normalized display lines, got %q", diff)
	}
	if !strings.Contains(diff, "-  User old\n+  User new\n") {
		t.Fatalf("missing CRLF host change:\n%s", diff)
	}
	if strings.Contains(diff, "No newline at end of file") {
		t.Fatalf("final CRLF should remain final-newline encoded:\n%s", diff)
	}
	assertPreviewReadOnly(t, path, original)
}

func TestPreviewHostMarksMissingFinalNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host target\n  HostName 10.0.0.1\n  User old"
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "target", Edit{User: previewStringPtr("new")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(diff, "\\ No newline at end of file\n") != 2 {
		t.Fatalf("expected old and new no-newline markers:\n%s", diff)
	}
	assertPreviewReadOnly(t, path, original)
}

func TestPreviewHostRejectsInvalidPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "Host target\n  HostName 10.0.0.1\n"
	writeFile(t, path, original)

	diff, err := PreviewHost(path, "target", Edit{Port: previewIntPtr(70000)})
	if err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Fatalf("expected invalid port error, got diff=%q err=%v", diff, err)
	}
	if diff != "" {
		t.Fatalf("expected empty diff on error, got:\n%s", diff)
	}
	assertPreviewReadOnly(t, path, original)
}

func assertPreviewOnlyChangesHost(t *testing.T, diff, untouchedAlias string) {
	t.Helper()
	for _, line := range strings.Split(diff, "\n") {
		if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
			!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") &&
			strings.Contains(line, untouchedAlias) {
			t.Fatalf("diff changed untouched host %q:\n%s", untouchedAlias, diff)
		}
	}
}

func assertPreviewReadOnly(t *testing.T, path, original string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("preview changed %s:\n%s", path, content)
	}
	for _, pattern := range []string{path + ".bak.*", path + ".lock", filepath.Join(filepath.Dir(path), ".whop-config-*")} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("preview created files matching %s: %#v", pattern, matches)
		}
	}
}

func TestPreviewHashRejectsStaleApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "Host target\n  HostName 10.0.0.1\n  User old\n")
	preview, err := PreviewHostChange(path, "target", Edit{User: previewStringPtr("new")})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Diff == "" || preview.Hash == "" {
		t.Fatalf("preview = %+v", preview)
	}
	writeFile(t, path, "Host target\n  HostName 10.0.0.1\n  User changed-by-someone-else\n")
	err = EditHost(path, "target", Edit{User: previewStringPtr("new"), ExpectedHash: preview.Hash})
	if !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("expected ErrConfigChanged, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "changed-by-someone-else") {
		t.Fatalf("stale apply modified file:\n%s", content)
	}
	if backups, _ := filepath.Glob(path + ".bak.*"); len(backups) != 0 {
		t.Fatalf("stale apply created backup: %#v", backups)
	}
}
