package cli

import (
	"strings"
	"testing"

	"whop/internal/sshconfig"
)

func TestSFTPCommandStartsBrowser(t *testing.T) {
	td := newTestDeps()
	var gotHost string
	td.deps.RunSFTP = func(host sshconfig.Host, _ []sshconfig.Host, _ bool) error {
		gotHost = host.Alias
		return nil
	}
	if code := Run([]string{"sftp", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, td.stderr.String())
	}
	if gotHost != "prod-db" {
		t.Fatalf("host = %q", gotHost)
	}
}

func TestSFTPCommandRequiresAlias(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"sftp"}, td.deps); code != 2 {
		t.Fatalf("exit code = %d, stderr=%q", code, td.stderr.String())
	}
	if !strings.Contains(td.stderr.String(), "usage: whop sftp") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}
