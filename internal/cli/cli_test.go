package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"ttydeck/internal/sshconfig"
	"ttydeck/internal/ui"
)

func cliHosts() []sshconfig.Host {
	return []sshconfig.Host{
		{Alias: "prod-db", HostName: "10.0.0.11", User: "deploy", Port: 22, Tags: []string{"prod", "db"}, Kind: sshconfig.KindServer},
		{Alias: "prod-cache", HostName: "10.0.0.12", User: "deploy", Port: 2222, Tags: []string{"prod"}, Kind: sshconfig.KindServer},
		{Alias: "github.com", HostName: "github.com", User: "git", Port: 22, Kind: sshconfig.KindGitAuth},
	}
}

type testDeps struct {
	deps      Deps
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	connected string
	tuiOpts   ui.Options
	tuiRan    bool
	loadPath  string
}

func newTestDeps() *testDeps {
	td := &testDeps{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	td.deps = Deps{
		Stdin:  strings.NewReader(""),
		Stdout: td.stdout,
		Stderr: td.stderr,
		LoadConfig: func(path string) ([]sshconfig.Host, error) {
			td.loadPath = path
			return cliHosts(), nil
		},
		Connect: func(host sshconfig.Host, _ io.Reader, _, _ io.Writer) error {
			td.connected = host.Alias
			return nil
		},
		RunTUI: func(opts ui.Options) error {
			td.tuiRan = true
			td.tuiOpts = opts
			return nil
		},
	}
	return td
}

func TestVersion(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"version"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stdout.String(), "ttydeck "+Version) {
		t.Fatalf("stdout = %q", td.stdout.String())
	}
}

func TestListServersHidesGitAuthByDefault(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"list"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	out := td.stdout.String()
	if !strings.Contains(out, "prod-db") || !strings.Contains(out, "10.0.0.11") {
		t.Fatalf("missing server: %q", out)
	}
	if strings.Contains(out, "github.com") {
		t.Fatalf("git auth host should be hidden: %q", out)
	}
}

func TestListAll(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"list", "-all"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stdout.String(), "github.com") {
		t.Fatalf("all mode missing git auth host: %q", td.stdout.String())
	}
}

func TestListJSON(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"list", "-json"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var hosts []sshconfig.Host
	if err := json.Unmarshal(td.stdout.Bytes(), &hosts); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, td.stdout.String())
	}
	if len(hosts) != 2 || hosts[0].Alias != "prod-db" {
		t.Fatalf("unexpected hosts: %#v", hosts)
	}
}

func TestListEmptyJSONIsArray(t *testing.T) {
	td := newTestDeps()
	td.deps.LoadConfig = func(string) ([]sshconfig.Host, error) { return nil, nil }
	if code := Run([]string{"list", "-json", "-all"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(td.stdout.String()) != "[]" {
		t.Fatalf("expected [] got %q", td.stdout.String())
	}
}

func TestConnect(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"connect", "-config", "/tmp/ssh_config", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, td.stderr.String())
	}
	if td.connected != "prod-db" {
		t.Fatalf("connected = %q", td.connected)
	}
	if td.loadPath != "/tmp/ssh_config" {
		t.Fatalf("load path = %q", td.loadPath)
	}
}

func TestConnectUnknownHost(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"connect", "missing"}, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "not found") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestConnectUsage(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"connect"}, td.deps); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "usage:") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestTUIOptions(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"-config", "/tmp/ssh_config", "-all", "prod", "db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, td.stderr.String())
	}
	if !td.tuiRan {
		t.Fatal("tui did not run")
	}
	if td.tuiOpts.InitialFilter != "prod db" {
		t.Fatalf("initial filter = %q", td.tuiOpts.InitialFilter)
	}
	if !td.tuiOpts.ShowAll {
		t.Fatal("show all not set")
	}
	if td.tuiOpts.ConfigPath != "/tmp/ssh_config" {
		t.Fatalf("config path = %q", td.tuiOpts.ConfigPath)
	}
	if td.tuiOpts.Load == nil || td.tuiOpts.Connect == nil {
		t.Fatal("tui callbacks must be configured")
	}
	hosts, err := td.tuiOpts.Load()
	if err != nil || len(hosts) != 3 {
		t.Fatalf("load returned %#v, %v", hosts, err)
	}
}

func TestTUIError(t *testing.T) {
	td := newTestDeps()
	td.deps.RunTUI = func(ui.Options) error { return errors.New("tui failed") }
	if code := Run(nil, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "tui failed") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestLoadError(t *testing.T) {
	td := newTestDeps()
	td.deps.LoadConfig = func(string) ([]sshconfig.Host, error) { return nil, errors.New("load failed") }
	if code := Run([]string{"list"}, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "load failed") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestHelp(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"help"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stdout.String(), "Usage:") {
		t.Fatalf("stdout = %q", td.stdout.String())
	}
}
