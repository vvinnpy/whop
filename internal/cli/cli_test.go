package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ttydeck/internal/sshconfig"
	"ttydeck/internal/store"
	"ttydeck/internal/ui"
)

func cliHosts() []sshconfig.Host {
	return []sshconfig.Host{
		{Alias: "prod-db", HostName: "10.0.0.11", User: "deploy", Port: 22, Tags: []string{"prod", "db"}, Kind: sshconfig.KindServer},
		{Alias: "prod-cache", HostName: "10.0.0.12", User: "deploy", Port: 2222, Tags: []string{"prod"}, Kind: sshconfig.KindServer},
		{Alias: "github.com", HostName: "github.com", User: "git", Port: 22, Kind: sshconfig.KindGitAuth},
	}
}

type persistentTestStore struct {
	*store.Store
}

func (s persistentTestStore) Close() error { return nil }

type testDeps struct {
	deps      Deps
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	connected string
	tuiOpts   ui.Options
	tuiRan    bool
	tuiHosts  []sshconfig.Host
	tuiErr    error
	loadPath  string
	metaStore *store.Store
	recorded  bool
}

func newTestDeps() *testDeps {
	metaStore, err := store.Open(":memory:")
	if err != nil {
		panic(err)
	}
	td := &testDeps{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, metaStore: metaStore}
	td.deps = Deps{
		Stdin:  strings.NewReader(""),
		Stdout: td.stdout,
		Stderr: td.stderr,
		LoadConfig: func(path string) ([]sshconfig.Host, error) {
			td.loadPath = path
			return cliHosts(), nil
		},
		Connect: func(host sshconfig.Host, _ []sshconfig.Host, _ io.Reader, _, _ io.Writer, record bool) error {
			td.connected = host.Alias
			td.recorded = record
			return nil
		},
		RunTUI: func(opts ui.Options) error {
			td.tuiRan = true
			td.tuiOpts = opts
			if opts.Load != nil {
				td.tuiHosts, td.tuiErr = opts.Load()
			}
			return nil
		},
		OpenStore: func() (MetaStore, error) {
			return persistentTestStore{td.metaStore}, nil
		},
		RecordingDir: filepath.Join(os.TempDir(), "ttydeck-test-recordings"),
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
	if code := Run([]string{"-config", "/tmp/ssh_config", "-all", "-record", "prod", "db"}, td.deps); code != 0 {
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
	if !td.tuiOpts.Record {
		t.Fatal("record flag not set")
	}
	if td.tuiOpts.ConfigPath != "/tmp/ssh_config" {
		t.Fatalf("config path = %q", td.tuiOpts.ConfigPath)
	}
	if td.tuiOpts.Load == nil || td.tuiOpts.Connect == nil {
		t.Fatal("tui callbacks must be configured")
	}
	if td.tuiErr != nil || len(td.tuiHosts) != 3 {
		t.Fatalf("load returned %#v, %v", td.tuiHosts, td.tuiErr)
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

func TestVersionAliases(t *testing.T) {
	for _, arg := range []string{"--version", "-v"} {
		td := newTestDeps()
		if code := Run([]string{arg}, td.deps); code != 0 {
			t.Fatalf("%s exit code = %d", arg, code)
		}
		if !strings.Contains(td.stdout.String(), Version) {
			t.Fatalf("%s stdout = %q", arg, td.stdout.String())
		}
	}
}

func TestSubcommandHelp(t *testing.T) {
	for _, args := range [][]string{{"list", "-h"}, {"list", "--help"}, {"connect", "-h"}, {"connect", "--help"}} {
		td := newTestDeps()
		if code := Run(args, td.deps); code != 0 {
			t.Fatalf("%v exit code = %d", args, code)
		}
	}
}

func TestListNoHosts(t *testing.T) {
	td := newTestDeps()
	td.deps.LoadConfig = func(string) ([]sshconfig.Host, error) { return nil, nil }
	if code := Run([]string{"list"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stdout.String(), "no hosts found") {
		t.Fatalf("stdout = %q", td.stdout.String())
	}
}

func TestBareInvocationRunsTUI(t *testing.T) {
	td := newTestDeps()
	if code := Run(nil, td.deps); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, td.stderr.String())
	}
	if !td.tuiRan {
		t.Fatal("bare invocation should run the TUI")
	}
}

func TestFavoriteNoteAndTagCommands(t *testing.T) {
	td := newTestDeps()

	if code := Run([]string{"favorite", "prod-db", "on"}, td.deps); code != 0 {
		t.Fatalf("favorite exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"list"}, td.deps); code != 0 {
		t.Fatalf("list exit = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "*") {
		t.Fatalf("favorite marker missing: %q", td.stdout.String())
	}

	td.stderr.Reset()
	if code := Run([]string{"note", "prod-db", "primary", "database"}, td.deps); code != 0 {
		t.Fatalf("note exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"note", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("note read exit = %d: %s", code, td.stderr.String())
	}
	if strings.TrimSpace(td.stdout.String()) != "primary database" {
		t.Fatalf("note = %q", td.stdout.String())
	}

	td.stderr.Reset()
	if code := Run([]string{"tag", "prod-db", "prod", "db"}, td.deps); code != 0 {
		t.Fatalf("tag exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"tag", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("tag read exit = %d: %s", code, td.stderr.String())
	}
	if strings.TrimSpace(td.stdout.String()) != "db,prod" {
		t.Fatalf("tags = %q", td.stdout.String())
	}
}

func TestConnectRecordsLastConnection(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"connect", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("connect exit = %d: %s", code, td.stderr.String())
	}
	meta, err := td.metaStore.Get("prod-db")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ConnectCount != 1 || meta.LastConnectedAt.IsZero() {
		t.Fatalf("connection not recorded: %+v", meta)
	}
}

func TestConnectRecordFlag(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"connect", "-record", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	if !td.recorded {
		t.Fatal("record flag was not passed to connector")
	}
}

func TestReplayCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.cast")
	cast := `{"version":2,"width":80,"height":24,"timestamp":0}
[0,"o","hello replay\n"]
`
	if err := os.WriteFile(path, []byte(cast), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	if code := Run([]string{"replay", "-speed", "1000", path}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "hello replay") {
		t.Fatalf("replay output = %q", td.stdout.String())
	}
}

func TestSearchCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(path, []byte("line one\nprod database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.RecordingDir = dir
	if code := Run([]string{"search", "prod"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), path+":2: prod database") {
		t.Fatalf("search output = %q", td.stdout.String())
	}
}
