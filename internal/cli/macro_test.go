package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ttydeck/internal/automation"
	sshconn "ttydeck/internal/connector/ssh"
	"ttydeck/internal/sshconfig"
	"ttydeck/internal/ui"
)

func TestMacroLifecycleAndRun(t *testing.T) {
	td := newTestDeps()
	path := filepath.Join(t.TempDir(), "login.json")
	definition := `{
  "version": 1,
  "steps": [
    {"name": "prompt", "kind": "wait", "pattern": "login: ", "timeout": "2s", "retries": 1},
    {"name": "user", "kind": "send", "send": "admin", "newline": true},
    {"name": "shell", "kind": "wait", "pattern": "shell> ", "timeout": "2s"},
    {"name": "command", "kind": "send", "send": "uptime\n"}
  ]
}`
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"macro", "set", "login", path}, td.deps); code != 0 {
		t.Fatalf("set exit = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "macro login saved (4 steps)") {
		t.Fatalf("set output = %q", td.stdout.String())
	}

	td.stdout.Reset()
	if code := Run([]string{"macro", "list"}, td.deps); code != 0 {
		t.Fatalf("list exit = %d: %s", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "login\t4 steps") {
		t.Fatalf("list output = %q", td.stdout.String())
	}

	td.stdout.Reset()
	if code := Run([]string{"macro", "show", "login"}, td.deps); code != 0 {
		t.Fatalf("show exit = %d: %s", code, td.stderr.String())
	}
	var shown macroDefinition
	if err := json.Unmarshal(td.stdout.Bytes(), &shown); err != nil {
		t.Fatalf("show output is not JSON: %v\n%s", err, td.stdout.String())
	}
	if shown.Name != "login" || len(shown.Steps) != 4 {
		t.Fatalf("shown = %+v", shown)
	}

	var ranMacro automation.Macro
	td.deps.RunMacro = func(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, macro automation.Macro, output, _ io.Writer, useAgent bool) error {
		if host.Alias != "prod-db" || len(hosts) != len(cliHosts()) {
			t.Fatalf("unexpected host context: %+v / %+v", host, hosts)
		}
		ranMacro = macro
		_, _ = io.WriteString(output, "remote output\n")
		return nil
	}
	td.stdout.Reset()
	td.stderr.Reset()
	if code := Run([]string{"macro", "run", "-timeout", "5s", "login", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("run exit = %d: %s", code, td.stderr.String())
	}
	if ranMacro.Name != "login" || len(ranMacro.Steps) != 4 {
		t.Fatalf("ran macro = %+v", ranMacro)
	}
	if string(ranMacro.Steps[1].Send) != "admin\n" || ranMacro.Steps[1].Kind != automation.StepSend {
		t.Fatalf("send step = %+v", ranMacro.Steps[1])
	}
	if ranMacro.Steps[0].Timeout != 2*time.Second || ranMacro.Steps[0].MaxRetries != 1 {
		t.Fatalf("wait step = %+v", ranMacro.Steps[0])
	}
	if !strings.Contains(td.stdout.String(), "remote output") {
		t.Fatalf("run output = %q", td.stdout.String())
	}
	meta, err := td.metaStore.Get("prod-db")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ConnectCount != 1 {
		t.Fatalf("macro run was not recorded: %+v", meta)
	}

	td.stdout.Reset()
	if code := Run([]string{"macro", "delete", "login"}, td.deps); code != 0 {
		t.Fatalf("delete exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"macro", "list", "-json"}, td.deps); code != 0 {
		t.Fatalf("list json exit = %d: %s", code, td.stderr.String())
	}
	if strings.TrimSpace(td.stdout.String()) != "[]" {
		t.Fatalf("list after delete = %q", td.stdout.String())
	}
}

func TestMacroSecretUsesEnvironmentOnly(t *testing.T) {
	td := newTestDeps()
	path := filepath.Join(t.TempDir(), "secret.json")
	definition := `{"version":1,"steps":[
		{"kind":"wait","pattern":"Password: ","timeout":"2s"},
		{"kind":"send","secret_env":"TTYDECK_TEST_PASSWORD","newline":true},
		{"kind":"wait","pattern":"shell> ","timeout":"2s"}
	]}`
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"macro", "set", "secret-login", path}, td.deps); code != 0 {
		t.Fatalf("set exit = %d: %s", code, td.stderr.String())
	}
	raw, found, err := td.metaStore.MacroDefinition("secret-login")
	if err != nil || !found {
		t.Fatalf("stored macro: found=%v err=%v", found, err)
	}
	if strings.Contains(string(raw), "super-secret") || !strings.Contains(string(raw), "TTYDECK_TEST_PASSWORD") {
		t.Fatalf("stored definition leaked or omitted secret source: %s", raw)
	}

	td.deps.LookupEnv = func(name string) (string, bool) {
		if name == "TTYDECK_TEST_PASSWORD" {
			return "super-secret", true
		}
		return "", false
	}
	var sent automation.Step
	td.deps.RunMacro = func(_ context.Context, _ sshconfig.Host, _ []sshconfig.Host, macro automation.Macro, _, _ io.Writer, _ bool) error {
		sent = macro.Steps[1]
		return nil
	}
	if code := Run([]string{"macro", "run", "secret-login", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("run exit = %d: %s", code, td.stderr.String())
	}
	if string(sent.Send) != "super-secret\n" || !sent.Secret {
		t.Fatalf("secret step = %+v", sent)
	}
	if strings.Contains(td.stdout.String()+td.stderr.String(), "super-secret") {
		t.Fatalf("secret leaked to CLI output: stdout=%q stderr=%q", td.stdout.String(), td.stderr.String())
	}

	td.stderr.Reset()
	td.deps.LookupEnv = func(string) (string, bool) { return "", false }
	if code := Run([]string{"macro", "run", "secret-login", "prod-db"}, td.deps); code != 1 {
		t.Fatalf("missing env exit = %d", code)
	}
	if !strings.Contains(td.stderr.String(), "TTYDECK_TEST_PASSWORD is not set") {
		t.Fatalf("stderr = %q", td.stderr.String())
	}
}

func TestMacroValidationErrors(t *testing.T) {
	tests := []struct {
		name       string
		definition string
		want       string
	}{
		{name: "unknown field", definition: `{"steps":[{"kind":"send","send":"x","password":"secret"}]}`, want: "unknown field"},
		{name: "no steps", definition: `{"steps":[]}`, want: "no steps"},
		{name: "bad timeout", definition: `{"steps":[{"kind":"wait","pattern":"x","timeout":"soon"}]}`, want: "invalid timeout"},
		{name: "both send sources", definition: `{"steps":[{"kind":"send","send":"x","secret_env":"PASSWORD"}]}`, want: "mutually exclusive"},
		{name: "bad env", definition: `{"steps":[{"kind":"send","secret_env":"bad-name"}]}`, want: "invalid secret_env"},
		{name: "trailing json", definition: `{"steps":[{"kind":"send","send":"x"}]} {}`, want: "trailing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			td := newTestDeps()
			path := filepath.Join(t.TempDir(), "macro.json")
			if err := os.WriteFile(path, []byte(test.definition), 0o600); err != nil {
				t.Fatal(err)
			}
			if code := Run([]string{"macro", "set", "bad", path}, td.deps); code != 1 {
				t.Fatalf("exit = %d, stderr=%q", code, td.stderr.String())
			}
			if !strings.Contains(td.stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", td.stderr.String(), test.want)
			}
		})
	}
}

func TestTUIExposesStoredMacros(t *testing.T) {
	td := newTestDeps()
	definition := []byte(`{"version":1,"name":"collect","steps":[{"kind":"send","send":"uptime\n"}]}`)
	if err := td.metaStore.SetMacroDefinition("collect", definition); err != nil {
		t.Fatal(err)
	}
	var names []string
	td.deps.RunTUI = func(opts ui.Options) error {
		if opts.LoadMacros == nil || opts.RunMacro == nil {
			t.Fatal("TUI macro hooks are not configured")
		}
		var err error
		names, err = opts.LoadMacros()
		return err
	}
	if code := Run(nil, td.deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, td.stderr.String())
	}
	if len(names) != 1 || names[0] != "collect" {
		t.Fatalf("macro names = %#v", names)
	}
}

func TestMacroRunJSONDiagnostic(t *testing.T) {
	td := newTestDeps()
	path := filepath.Join(t.TempDir(), "timeout.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"steps":[{"kind":"wait","pattern":"never","timeout":"1s"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"macro", "set", "timeout", path}, td.deps); code != 0 {
		t.Fatalf("set exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	td.stderr.Reset()
	td.deps.RunMacro = func(_ context.Context, _ sshconfig.Host, _ []sshconfig.Host, _ automation.Macro, _, _ io.Writer, _ bool) error {
		return &sshconn.StageError{Stage: "automation", Host: "prod-db", Err: automation.ErrTimeout}
	}
	if code := Run([]string{"macro", "run", "-json", "timeout", "prod-db"}, td.deps); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	var diagnostic sshconn.Diagnostic
	if err := json.Unmarshal(td.stdout.Bytes(), &diagnostic); err != nil {
		t.Fatalf("invalid diagnostic JSON: %v\n%s", err, td.stdout.String())
	}
	if diagnostic.Stage != "automation" || diagnostic.Category != "automation_timeout" {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
}

func TestMacroRejectsControlCharactersAndExtraArguments(t *testing.T) {
	td := newTestDeps()
	path := filepath.Join(t.TempDir(), "safe.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"steps":[{"kind":"send","send":"uptime\n"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"macro", "set", "bad\nname", path}, td.deps); code != 2 {
		t.Fatalf("control name exit = %d", code)
	}
	if code := Run([]string{"macro", "set", "ok", path, "extra"}, td.deps); code != 2 {
		t.Fatalf("extra args exit = %d", code)
	}
	if code := Run([]string{"macro", "list", "extra"}, td.deps); code != 2 {
		t.Fatalf("list extra args exit = %d", code)
	}
}

func TestMacroNewlineIsNotDuplicated(t *testing.T) {
	td := newTestDeps()
	path := filepath.Join(t.TempDir(), "newline.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"steps":[{"kind":"send","send":"uptime\n","newline":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"macro", "set", "newline", path}, td.deps); code != 0 {
		t.Fatalf("set exit = %d: %s", code, td.stderr.String())
	}
	macro, err := loadStoredMacro(td.metaStore, "newline", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(macro.Steps[0].Send); got != "uptime\n" {
		t.Fatalf("send = %q", got)
	}
}
