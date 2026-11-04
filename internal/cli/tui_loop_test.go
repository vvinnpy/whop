package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"whop/internal/automation"
	"whop/internal/sshconfig"
	"whop/internal/ui"
)

func TestTUIConnectRelaunchesInsteadOfTeaExec(t *testing.T) {
	td := newTestDeps()
	t.Setenv("WHOP_TUI_NOTICE", "")
	calls := 0
	td.deps.RunTUI = func(opts ui.Options) error {
		calls++
		hosts, err := opts.Load()
		if err != nil || len(hosts) == 0 {
			t.Fatalf("load hosts: %v %#v", err, hosts)
		}
		cmd := opts.Connect(hosts[0], false)
		if cmd == nil {
			t.Fatal("connect command is nil")
		}
		if msg := cmd(); msg == nil {
			t.Fatal("connect should quit the TUI")
		}
		return nil
	}
	if code := Run(nil, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	if calls != 1 {
		t.Fatalf("RunTUI calls = %d, want 1", calls)
	}
	if td.connected != "prod-db" {
		t.Fatalf("connected = %q", td.connected)
	}
	if !td.relaunched {
		t.Fatal("TUI was not relaunched")
	}
	if got := os.Getenv("WHOP_TUI_NOTICE"); got != "session ended" {
		t.Fatalf("notice = %q", got)
	}
}

func TestTUIMacroRelaunchesInsteadOfTeaExec(t *testing.T) {
	td := newTestDeps()
	if err := td.metaStore.SetMacroDefinition("collect", []byte(`{"version":1,"name":"collect","steps":[{"kind":"send","send":"uptime\n"}]}`)); err != nil {
		t.Fatal(err)
	}
	td.deps.RunMacro = func(_ context.Context, host sshconfig.Host, _ []sshconfig.Host, _ automation.Macro, _, _ io.Writer, _ automation.EventFunc, _ bool) error {
		td.ranHosts = append(td.ranHosts, host.Alias)
		return nil
	}
	t.Setenv("WHOP_TUI_NOTICE", "")
	calls := 0
	td.deps.RunTUI = func(opts ui.Options) error {
		calls++
		hosts, err := opts.Load()
		if err != nil || len(hosts) == 0 {
			t.Fatalf("load hosts: %v %#v", err, hosts)
		}
		cmd := opts.RunMacro(hosts[0], "collect")
		if cmd == nil {
			t.Fatal("macro command is nil")
		}
		cmd()
		return nil
	}
	if code := Run(nil, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	if calls != 1 || !td.relaunched {
		t.Fatalf("calls=%d relaunched=%v", calls, td.relaunched)
	}
	if got := os.Getenv("WHOP_TUI_NOTICE"); !strings.Contains(got, "macro collect completed on prod-db") {
		t.Fatalf("notice = %q", got)
	}
	if len(td.ranHosts) != 1 || td.ranHosts[0] != "prod-db" {
		t.Fatalf("macro host = %#v", td.ranHosts)
	}
}
