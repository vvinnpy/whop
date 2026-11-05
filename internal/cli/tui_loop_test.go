package cli

import (
	"context"
	"fmt"
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
	t.Setenv(tuiNoticeEnv, "")
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
	if got := os.Getenv(tuiNoticeEnv); got != "session ended · prod-db" {
		t.Fatalf("notice = %q", got)
	}
	if got := os.Getenv(tuiNoticeLevelEnv); got != "result" {
		t.Fatalf("notice level = %q", got)
	}
	if got := os.Getenv(tuiNoticeScopeEnv); got != "prod-db" {
		t.Fatalf("notice scope = %q", got)
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
	t.Setenv(tuiNoticeEnv, "")
	t.Setenv(tuiMacroPauseEnv, "")
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
	if got := os.Getenv(tuiNoticeEnv); got != "macro collect completed · prod-db" {
		t.Fatalf("notice = %q", got)
	}
	if got := os.Getenv(tuiMacroPauseEnv); got != "1" {
		t.Fatalf("pause flag = %q", got)
	}
	if len(td.ranHosts) != 1 || td.ranHosts[0] != "prod-db" {
		t.Fatalf("macro host = %#v", td.ranHosts)
	}
}

func TestTUIMacroRelaunchWaitsForEnter(t *testing.T) {
	td := newTestDeps()
	td.deps.Stdin = strings.NewReader("\n")
	t.Setenv(tuiNoticeEnv, "macro collect completed · prod-db")
	t.Setenv(tuiNoticeLevelEnv, "result")
	t.Setenv(tuiNoticeScopeEnv, "prod-db")
	t.Setenv(tuiMacroPauseEnv, "1")

	td.deps.RunTUI = func(opts ui.Options) error {
		td.tuiRan = true
		if opts.InitialNotice.Text != "macro collect completed · prod-db" || opts.InitialNotice.Scope != "prod-db" {
			t.Fatalf("initial notice = %+v", opts.InitialNotice)
		}
		if !strings.Contains(td.stderr.String(), "Press Enter to return to the host list...") {
			t.Fatalf("missing pause prompt: %q", td.stderr.String())
		}
		if _, exists := os.LookupEnv(tuiMacroPauseEnv); exists {
			t.Fatal("pause flag should be consumed before the TUI starts")
		}
		return nil
	}

	if code := Run(nil, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	if !td.tuiRan {
		t.Fatal("TUI was not started after the pause")
	}
	if !strings.Contains(td.stderr.String(), "macro collect completed · prod-db") {
		t.Fatalf("missing macro result: %q", td.stderr.String())
	}
}

func TestTUIConnectFailureRelaunchesWithErrorNotice(t *testing.T) {
	td := newTestDeps()
	td.deps.Connect = func(sshconfig.Host, []sshconfig.Host, io.Reader, io.Writer, io.Writer, bool, bool) error {
		return fmt.Errorf("permission denied")
	}
	t.Setenv(tuiNoticeEnv, "")
	t.Setenv(tuiNoticeLevelEnv, "")
	t.Setenv(tuiNoticeScopeEnv, "")
	td.deps.RunTUI = func(opts ui.Options) error {
		hosts, err := opts.Load()
		if err != nil {
			t.Fatal(err)
		}
		cmd := opts.Connect(hosts[0], false)
		if cmd == nil {
			t.Fatal("connect command is nil")
		}
		_ = cmd()
		return nil
	}
	if code := Run(nil, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	if !td.relaunched {
		t.Fatal("TUI was not relaunched")
	}
	if got := os.Getenv(tuiNoticeEnv); got != "connection failed · prod-db: permission denied" {
		t.Fatalf("notice = %q", got)
	}
	if got := os.Getenv(tuiNoticeLevelEnv); got != "error" {
		t.Fatalf("notice level = %q", got)
	}
	if got := os.Getenv(tuiNoticeScopeEnv); got != "prod-db" {
		t.Fatalf("notice scope = %q", got)
	}
}
