package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sshconn "whop/internal/connector/ssh"
	"whop/internal/sshconfig"
	"whop/internal/store"
	"whop/internal/ui"
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

type fakePreparedConnect struct {
	deps      *testDeps
	host      sshconfig.Host
	record    bool
	useAgent  bool
	runInput  string
	runOut    string
	runErrOut string
	runErr    error
}

func (p *fakePreparedConnect) Run(_ context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	p.deps.connected = p.host.Alias
	p.deps.recorded = p.record
	p.deps.useAgent = p.useAgent
	if p.runInput != "" {
		_, _ = io.CopyN(io.Discard, stdin, int64(len(p.runInput)))
	}
	if p.runOut != "" {
		_, _ = io.WriteString(stdout, p.runOut)
	}
	if p.runErrOut != "" {
		_, _ = io.WriteString(stderr, p.runErrOut)
	}
	return p.runErr
}

func (p *fakePreparedConnect) Close() error { return nil }

type testDeps struct {
	deps           Deps
	stdout         *bytes.Buffer
	stderr         *bytes.Buffer
	connected      string
	tuiOpts        ui.Options
	tuiRan         bool
	tuiHosts       []sshconfig.Host
	tuiErr         error
	loadPath       string
	metaStore      *store.Store
	recorded       bool
	zmPutHost      string
	zmPutFiles     []string
	zmPutOverwrite bool
	zmGetHost      string
	zmGetFiles     []string
	zmGetDest      string
	zmGetMax       int64
	zmGetOverwrite bool
	ranHosts       []string
	ranCommands    []string
	useAgent       bool
	relaunched     bool
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
		Connect: func(host sshconfig.Host, _ []sshconfig.Host, _ io.Reader, _, _ io.Writer, record bool, useAgent bool) error {
			td.connected = host.Alias
			td.recorded = record
			td.useAgent = useAgent
			return nil
		},
		PrepareConnect: func(_ context.Context, host sshconfig.Host, _ []sshconfig.Host, record, useAgent bool) (PreparedConnect, error) {
			return &fakePreparedConnect{deps: td, host: host, record: record, useAgent: useAgent}, nil
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
		RecordingDir: filepath.Join(os.TempDir(), "whop-test-recordings"),
		ZmodemPut: func(host sshconfig.Host, _ []sshconfig.Host, files []string, overwrite bool, useAgent bool, _ io.Writer) error {
			td.zmPutHost = host.Alias
			td.zmPutFiles = append([]string(nil), files...)
			td.zmPutOverwrite = overwrite
			return nil
		},
		Diagnose: func(sshconfig.Host, []sshconfig.Host, bool) error { return nil },
		RunCommand: func(host sshconfig.Host, _ []sshconfig.Host, command string, useAgent bool) (string, error) {
			td.ranHosts = append(td.ranHosts, host.Alias)
			td.ranCommands = append(td.ranCommands, command)
			return "ran " + command + " on " + host.Alias + "\n", nil
		},
		Relaunch: func() error {
			td.relaunched = true
			return nil
		},
		ZmodemGet: func(host sshconfig.Host, _ []sshconfig.Host, files []string, dest string, maxSize int64, overwrite bool, useAgent bool, _ io.Writer) error {
			td.zmGetHost = host.Alias
			td.zmGetFiles = append([]string(nil), files...)
			td.zmGetDest = dest
			td.zmGetMax = maxSize
			td.zmGetOverwrite = overwrite
			return nil
		},
	}
	return td
}

func TestVersion(t *testing.T) {
	oldVersion := Version
	Version = "v9.9.9"
	t.Cleanup(func() { Version = oldVersion })

	td := newTestDeps()
	if code := Run([]string{"version"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if got := td.stdout.String(); got != "whop v9.9.9\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestVersionFallsBackToBuildInfo(t *testing.T) {
	oldVersion := Version
	Version = ""
	t.Cleanup(func() { Version = oldVersion })

	if got := versionString(); got == "" || got == "v" {
		t.Fatalf("versionString() = %q", got)
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
	if td.tuiOpts.EditHost == nil || td.tuiOpts.SetTags == nil || td.tuiOpts.SaveNotes == nil {
		t.Fatal("tui metadata callbacks must be configured")
	}
	if td.tuiOpts.SetLoginMode == nil {
		t.Fatal("tui login access callback must be configured")
	}
	if td.tuiOpts.PreviewAdd == nil || td.tuiOpts.AddHost == nil || td.tuiOpts.RenameHost == nil || td.tuiOpts.SetHostPassword == nil {
		t.Fatal("tui host management callbacks must be configured")
	}
	if td.tuiErr != nil || len(td.tuiHosts) != 3 {
		t.Fatalf("load returned %#v, %v", td.tuiHosts, td.tuiErr)
	}
}

func TestTUIAddAndRenameHostCallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("Host old\n  HostName 10.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	if code := Run([]string{"-config", path}, td.deps); code != 0 {
		t.Fatalf("TUI exit = %d: %s", code, td.stderr.String())
	}
	imp := sshconfig.HostImport{Alias: "new-host", HostName: "10.0.0.2", User: "deploy", Port: 22}
	preview, err := td.tuiOpts.PreviewAdd(imp)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Diff == "" {
		t.Fatal("create preview is empty")
	}
	if err := td.tuiOpts.AddHost(imp, preview.Hash); err != nil {
		t.Fatal(err)
	}
	hosts, err := sshconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sshconfig.Find(hosts, "new-host"); !ok {
		t.Fatal("new host was not written")
	}

	if err := td.metaStore.SetFavorite("old", true); err != nil {
		t.Fatal(err)
	}
	if err := td.tuiOpts.RenameHost("old", "renamed"); err != nil {
		t.Fatal(err)
	}
	meta, err := td.metaStore.Get("renamed")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Favorite {
		t.Fatalf("renamed metadata = %+v", meta)
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
	out := td.stdout.String()
	for _, want := range []string{
		"Usage:", "Commands:", "Connection:", "Sessions:",
		"Metadata and automation:", "Configuration and security:",
		"list", "connect", "forward", "sftp", "snippet", "macro", "vault", "version",
		`Run "whop help <command>"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Flags for list:") {
		t.Fatalf("top-level help should not dump every command's flags:\n%s", out)
	}
}

func TestHelpCommand(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"help", "forward"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	out := td.stdout.String()
	for _, want := range []string{"whop forward - ", "Usage:", "-L spec", "-R spec", "-D spec", "Examples:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("forward help missing %q:\n%s", want, out)
		}
	}
}

func TestHelpNestedCommand(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"help", "vault", "status"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	out := td.stdout.String()
	for _, want := range []string{"whop vault status - ", "Usage:", "-json"} {
		if !strings.Contains(out, want) {
			t.Fatalf("vault status help missing %q:\n%s", want, out)
		}
	}
}

func TestHelpUnknownCommand(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"help", "does-not-exist"}, td.deps); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestVersionAliases(t *testing.T) {
	oldVersion := Version
	Version = "v9.9.9"
	t.Cleanup(func() { Version = oldVersion })

	for _, arg := range []string{"--version", "-v"} {
		td := newTestDeps()
		if code := Run([]string{arg}, td.deps); code != 0 {
			t.Fatalf("%s exit code = %d", arg, code)
		}
		if !strings.Contains(td.stdout.String(), "whop v9.9.9") {
			t.Fatalf("%s stdout = %q", arg, td.stdout.String())
		}
	}
}

func TestSubcommandHelp(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"list", "-h"}, "whop list - "},
		{[]string{"list", "--help"}, "whop list - "},
		{[]string{"connect", "-h"}, "whop connect - "},
		{[]string{"connect", "--help"}, "whop connect - "},
		{[]string{"macro", "run", "--help"}, "whop macro run - "},
		{[]string{"snippet", "set", "-h"}, "whop snippet set - "},
		{[]string{"list", "-json", "--help"}, "whop list - "},
	}
	for _, tt := range tests {
		td := newTestDeps()
		if code := Run(tt.args, td.deps); code != 0 {
			t.Fatalf("%v exit code = %d: %s", tt.args, code, td.stderr.String())
		}
		if !strings.Contains(td.stdout.String(), tt.want) {
			t.Fatalf("%v stdout = %q", tt.args, td.stdout.String())
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

func resolvedCLITempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSearchCommand(t *testing.T) {
	dir := resolvedCLITempDir(t)
	path := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(path, []byte("line one\nprod database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.RecordingDir = dir
	if code := Run([]string{"search", "prod"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	output := td.stdout.String()
	if !strings.Contains(output, path) || !strings.Contains(output, ">>>prod<<<") {
		t.Fatalf("search output = %q", output)
	}
}

func TestSearchFilesCommand(t *testing.T) {
	dir := resolvedCLITempDir(t)
	path := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(path, []byte("line one\nprod database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.RecordingDir = dir
	if code := Run([]string{"search", "-files", "prod"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), path+":2: prod database") {
		t.Fatalf("search output = %q", td.stdout.String())
	}
}

func TestIndexCommand(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "prod-db-20261103-100000.000")
	if err := os.WriteFile(base+".cast", []byte(`{"version":2,"timestamp":1762164000,"env":{"WHOP_HOST":"prod-db"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".txt", []byte("backup completed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.RecordingDir = dir
	if code := Run([]string{"index", "-json"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	var result indexResult
	if err := json.Unmarshal(td.stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, td.stdout.String())
	}
	if result.Indexed != 1 || result.Directory != dir {
		t.Fatalf("result = %+v", result)
	}
	td.stdout.Reset()
	if code := Run([]string{"search", "-json", "backup"}, td.deps); code != 0 {
		t.Fatalf("search exit = %d: %s", code, td.stderr.String())
	}
	var matches []store.TranscriptMatch
	if err := json.Unmarshal(td.stdout.Bytes(), &matches); err != nil {
		t.Fatalf("invalid search JSON: %v\n%s", err, td.stdout.String())
	}
	if len(matches) != 1 || matches[0].Host != "prod-db" || !strings.Contains(matches[0].Snippet, "backup") {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestZmodemPutCommand(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"zmodem", "put", "-overwrite", "prod-db", "a.txt", "b.txt"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	if td.zmPutHost != "prod-db" || !td.zmPutOverwrite {
		t.Fatalf("unexpected put capture: %+v", td)
	}
	if len(td.zmPutFiles) != 2 || td.zmPutFiles[0] != "a.txt" || td.zmPutFiles[1] != "b.txt" {
		t.Fatalf("put files = %#v", td.zmPutFiles)
	}
}

func TestZmodemGetCommand(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"zmodem", "get", "-dest", "/tmp/dest", "-max-size", "1024", "-overwrite", "prod-db", "/remote/a", "/remote/b"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	if td.zmGetHost != "prod-db" || td.zmGetDest != "/tmp/dest" || td.zmGetMax != 1024 || !td.zmGetOverwrite {
		t.Fatalf("unexpected get capture: %+v", td)
	}
	if len(td.zmGetFiles) != 2 || td.zmGetFiles[0] != "/remote/a" || td.zmGetFiles[1] != "/remote/b" {
		t.Fatalf("get files = %#v", td.zmGetFiles)
	}
}

func TestZmodemUsage(t *testing.T) {
	for _, args := range [][]string{{"zmodem"}, {"zmodem", "wat"}, {"zmodem", "put"}, {"zmodem", "get"}} {
		td := newTestDeps()
		if code := Run(args, td.deps); code != 2 {
			t.Fatalf("%v exit = %d, stderr=%s", args, code, td.stderr.String())
		}
	}
}

func TestZmodemUnknownHost(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"zmodem", "put", "missing", "a.txt"}, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "not found") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestRemoteRunCommand(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"run", "prod-db", "echo", "hello"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	if len(td.ranHosts) != 1 || td.ranHosts[0] != "prod-db" {
		t.Fatalf("ran hosts = %#v", td.ranHosts)
	}
	if len(td.ranCommands) != 1 || td.ranCommands[0] != "echo hello" {
		t.Fatalf("ran commands = %#v", td.ranCommands)
	}
	if !strings.Contains(td.stdout.String(), "ran echo hello on prod-db") {
		t.Fatalf("stdout = %q", td.stdout.String())
	}
}

func TestSnippetLifecycle(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"snippet", "set", "disk", "df", "-h"}, td.deps); code != 0 {
		t.Fatalf("set exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"snippet", "list"}, td.deps); code != 0 {
		t.Fatalf("list exit = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "disk\tdf -h") {
		t.Fatalf("list output = %q", td.stdout.String())
	}

	td.stdout.Reset()
	if code := Run([]string{"snippet", "run", "disk", "prod-db", "prod-cache"}, td.deps); code != 0 {
		t.Fatalf("run exit = %d: %s", code, td.stderr.String())
	}
	if len(td.ranHosts) != 2 || td.ranHosts[0] != "prod-db" || td.ranHosts[1] != "prod-cache" {
		t.Fatalf("ran hosts = %#v", td.ranHosts)
	}
	if len(td.ranCommands) != 2 || td.ranCommands[0] != "df -h" {
		t.Fatalf("ran commands = %#v", td.ranCommands)
	}
	if !strings.Contains(td.stdout.String(), "== prod-db ==") || !strings.Contains(td.stdout.String(), "== prod-cache ==") {
		t.Fatalf("run output = %q", td.stdout.String())
	}

	if code := Run([]string{"snippet", "delete", "disk"}, td.deps); code != 0 {
		t.Fatalf("delete exit = %d: %s", code, td.stderr.String())
	}
}

func TestSnippetUnknown(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"snippet", "run", "missing", "prod-db"}, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "not found") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestSnippetUsage(t *testing.T) {
	for _, args := range [][]string{{"snippet"}, {"snippet", "wat"}, {"snippet", "set"}, {"snippet", "run"}} {
		td := newTestDeps()
		if code := Run(args, td.deps); code != 2 {
			t.Fatalf("%v exit = %d", args, code)
		}
	}
}

func TestConnectJSONErrorDiagnostic(t *testing.T) {
	td := newTestDeps()
	td.deps.Connect = func(sshconfig.Host, []sshconfig.Host, io.Reader, io.Writer, io.Writer, bool, bool) error {
		return fmt.Errorf("ssh: handshake failed: EOF")
	}
	if code := Run([]string{"connect", "-json", "prod-db"}, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	var diagnostic sshconn.Diagnostic
	if err := json.Unmarshal(td.stdout.Bytes(), &diagnostic); err != nil {
		t.Fatalf("invalid diagnostic json: %v\n%s", err, td.stdout.String())
	}
	if diagnostic.Stage != "handshake" || diagnostic.Category != "peer_closed" {
		t.Fatalf("unexpected diagnostic: %+v", diagnostic)
	}
}

func TestConnectHumanDiagnostic(t *testing.T) {
	td := newTestDeps()
	td.deps.Connect = func(sshconfig.Host, []sshconfig.Host, io.Reader, io.Writer, io.Writer, bool, bool) error {
		return fmt.Errorf("ssh: handshake failed: EOF")
	}
	if code := Run([]string{"connect", "prod-db"}, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"stage: handshake", "category: peer_closed", "hint:"} {
		if !strings.Contains(td.stderr.String(), want) {
			t.Fatalf("stderr missing %q: %s", want, td.stderr.String())
		}
	}
}

func TestDiagnoseJSONSuccess(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"diagnose", "-json", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d: %s", code, td.stderr.String())
	}
	var diagnostic sshconn.Diagnostic
	if err := json.Unmarshal(td.stdout.Bytes(), &diagnostic); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, td.stdout.String())
	}
	if diagnostic.Category != "ok" || diagnostic.Host != "prod-db" {
		t.Fatalf("unexpected diagnostic: %+v", diagnostic)
	}
}

func TestDiagnoseJSONError(t *testing.T) {
	td := newTestDeps()
	td.deps.Diagnose = func(sshconfig.Host, []sshconfig.Host, bool) error {
		return fmt.Errorf("ssh: handshake failed: EOF")
	}
	if code := Run([]string{"diagnose", "-json", "prod-db"}, td.deps); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	var diagnostic sshconn.Diagnostic
	if err := json.Unmarshal(td.stdout.Bytes(), &diagnostic); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, td.stdout.String())
	}
	if diagnostic.Stage != "handshake" || diagnostic.Category != "peer_closed" {
		t.Fatalf("unexpected diagnostic: %+v", diagnostic)
	}
}

func TestDiagnoseUsage(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"diagnose"}, td.deps); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
}

func TestAgentFlagDefaultOff(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"connect", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if td.useAgent {
		t.Fatal("ssh-agent must be disabled by default")
	}
}

func TestAgentFlagOn(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"-agent", "connect", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !td.useAgent {
		t.Fatal("-agent did not enable ssh-agent")
	}
}

func TestAgentEnvAndNoAgent(t *testing.T) {
	t.Setenv("WHOP_SSH_AGENT", "1")
	td := newTestDeps()
	if code := Run([]string{"-no-agent", "connect", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if td.useAgent {
		t.Fatal("-no-agent did not override env")
	}
}

func TestSearchMissingDirectoryDoesNotPruneIndex(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "prod-db-20261103-100000.000")
	if err := os.WriteFile(base+".cast", []byte(`{"version":2,"timestamp":1762164000,"env":{"WHOP_HOST":"prod-db"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".txt", []byte("backup completed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.RecordingDir = dir
	if code := Run([]string{"index"}, td.deps); code != 0 {
		t.Fatalf("index exit = %d: %s", code, td.stderr.String())
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	td.stderr.Reset()
	if code := Run([]string{"search", "backup"}, td.deps); code != 1 {
		t.Fatalf("missing dir search exit = %d", code)
	}
	td.stdout.Reset()
	td.stderr.Reset()
	if code := Run([]string{"search", "-no-sync", "backup"}, td.deps); code != 0 {
		t.Fatalf("no-sync search exit = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "prod-db") {
		t.Fatalf("existing index was lost: %q", td.stdout.String())
	}
}

func TestSearchFilesJSONEmptyIsArray(t *testing.T) {
	td := newTestDeps()
	td.deps.RecordingDir = t.TempDir()
	if code := Run([]string{"search", "-files", "-json", "missing"}, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	if strings.TrimSpace(td.stdout.String()) != "[]" {
		t.Fatalf("output = %q", td.stdout.String())
	}
}

func TestIndexPrunesDeletedTranscripts(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "prod-db-20261103-100000.000")
	if err := os.WriteFile(base+".cast", []byte(`{"version":2,"timestamp":1762164000,"env":{"WHOP_HOST":"prod-db"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".txt", []byte("backup completed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.RecordingDir = dir
	if code := Run([]string{"index"}, td.deps); code != 0 {
		t.Fatalf("first index exit = %d: %s", code, td.stderr.String())
	}
	if err := os.Remove(base + ".txt"); err != nil {
		t.Fatal(err)
	}
	td.stdout.Reset()
	if code := Run([]string{"index", "-json"}, td.deps); code != 0 {
		t.Fatalf("second index exit = %d: %s", code, td.stderr.String())
	}
	var result indexResult
	if err := json.Unmarshal(td.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Indexed != 0 || result.Removed != 1 {
		t.Fatalf("result = %+v", result)
	}
	td.stdout.Reset()
	if code := Run([]string{"search", "-no-sync", "backup"}, td.deps); code != 0 {
		t.Fatalf("search exit = %d: %s", code, td.stderr.String())
	}
	if strings.TrimSpace(td.stdout.String()) != "" {
		t.Fatalf("stale transcript still searchable: %q", td.stdout.String())
	}
}

func TestAccessOverridesDefaultListFiltering(t *testing.T) {
	td := newTestDeps()

	if code := Run([]string{"access", "github.com", "login"}, td.deps); code != 0 {
		t.Fatalf("access login exit = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "github.com access=login effective=login") {
		t.Fatalf("unexpected access output: %q", td.stdout.String())
	}

	td.stdout.Reset()
	if code := Run([]string{"list"}, td.deps); code != 0 {
		t.Fatalf("list exit = %d", code)
	}
	if !strings.Contains(td.stdout.String(), "github.com") {
		t.Fatalf("manual login override did not appear in default list: %q", td.stdout.String())
	}

	if code := Run([]string{"access", "prod-db", "service"}, td.deps); code != 0 {
		t.Fatalf("access service exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"list"}, td.deps); code != 0 {
		t.Fatalf("list exit = %d", code)
	}
	if strings.Contains(td.stdout.String(), "prod-db") {
		t.Fatalf("service override remained in default list: %q", td.stdout.String())
	}

	if code := Run([]string{"access", "prod-db", "auto"}, td.deps); code != 0 {
		t.Fatalf("access auto exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"list"}, td.deps); code != 0 {
		t.Fatalf("list exit = %d", code)
	}
	if !strings.Contains(td.stdout.String(), "prod-db") {
		t.Fatalf("auto override did not restore default filtering: %q", td.stdout.String())
	}
}

func TestAccessQueryShowsOverrideAndEffectiveMode(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"access", "github.com"}, td.deps); code != 0 {
		t.Fatalf("access query exit = %d: %s", code, td.stderr.String())
	}
	if got := strings.TrimSpace(td.stdout.String()); got != "github.com access=auto effective=service" {
		t.Fatalf("access query = %q", got)
	}
}

func TestListJSONIncludesEffectiveLoginability(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"access", "prod-db", "service"}, td.deps); code != 0 {
		t.Fatalf("access service exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"list", "-all", "-json"}, td.deps); code != 0 {
		t.Fatalf("list json exit = %d", code)
	}
	var hosts []struct {
		Alias      string `json:"Alias"`
		AccessMode string `json:"access_mode"`
		Loginable  bool   `json:"loginable"`
	}
	if err := json.Unmarshal(td.stdout.Bytes(), &hosts); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, td.stdout.String())
	}
	byAlias := make(map[string]struct {
		AccessMode string
		Loginable  bool
	})
	for _, host := range hosts {
		byAlias[host.Alias] = struct {
			AccessMode string
			Loginable  bool
		}{host.AccessMode, host.Loginable}
	}
	if got := byAlias["prod-db"]; got.AccessMode != "service" || got.Loginable {
		t.Fatalf("prod-db access = %+v", got)
	}
	if got := byAlias["github.com"]; got.AccessMode != "auto" || got.Loginable {
		t.Fatalf("github.com access = %+v", got)
	}
}

func TestAccessRejectsInvalidValueAndMissingHost(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"access", "prod-db", "maybe"}, td.deps); code != 2 {
		t.Fatalf("invalid value exit = %d, want 2", code)
	}
	if code := Run([]string{"access", "missing", "login"}, td.deps); code != 1 {
		t.Fatalf("missing host exit = %d, want 1", code)
	}
	if code := Run([]string{"access"}, td.deps); code != 2 {
		t.Fatalf("missing args exit = %d, want 2", code)
	}
}

func TestDefaultZmodemOptionsDebugEnv(t *testing.T) {
	t.Setenv("WHOP_ZMODEM_DEBUG", "1")
	options := defaultZmodemOptions()
	if options.Debug == nil {
		t.Fatal("debug writer was not enabled")
	}
	if options.Progress == nil {
		t.Fatal("progress writer is nil")
	}
}

func TestIndexCommandSkipsUnchangedTranscripts(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "prod-db-20261103-100000.000")
	if err := os.WriteFile(base+".cast", []byte(`{"version":2,"timestamp":1762164000,"env":{"WHOP_HOST":"prod-db"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".txt", []byte("backup completed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	td := newTestDeps()
	td.deps.RecordingDir = dir

	run := func() indexResult {
		t.Helper()
		td.stdout.Reset()
		if code := Run([]string{"index", "-json"}, td.deps); code != 0 {
			t.Fatalf("index exit = %d: %s", code, td.stderr.String())
		}
		var result indexResult
		if err := json.Unmarshal(td.stdout.Bytes(), &result); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, td.stdout.String())
		}
		return result
	}

	if result := run(); result.Indexed != 1 {
		t.Fatalf("first index = %+v", result)
	}
	if result := run(); result.Indexed != 0 {
		t.Fatalf("unchanged index = %+v", result)
	}

	if err := os.WriteFile(base+".txt", []byte("backup failed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := run(); result.Indexed != 1 {
		t.Fatalf("changed index = %+v", result)
	}
}
