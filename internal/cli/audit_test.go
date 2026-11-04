package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"ttydeck/internal/automation"
	sshconn "ttydeck/internal/connector/ssh"
	"ttydeck/internal/sshconfig"
	"ttydeck/internal/store"
)

func TestAuditCommandAndClear(t *testing.T) {
	td := newTestDeps()
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for _, event := range []store.AuditEvent{
		{Time: at, Type: "macro_started", Host: "prod-db", Macro: "deploy"},
		{Time: at.Add(time.Second), Type: "macro_completed", Host: "prod-db", Macro: "deploy"},
	} {
		if err := td.metaStore.AppendAudit(event); err != nil {
			t.Fatal(err)
		}
	}
	if code := Run([]string{"audit", "-json"}, td.deps); code != 0 {
		t.Fatalf("audit exit = %d: %s", code, td.stderr.String())
	}
	var events []store.AuditEvent
	if err := json.Unmarshal(td.stdout.Bytes(), &events); err != nil {
		t.Fatalf("invalid audit JSON: %v\n%s", err, td.stdout.String())
	}
	if len(events) != 2 || events[0].Type != "macro_completed" || events[0].Host != "prod-db" {
		t.Fatalf("events = %#v", events)
	}
	td.stdout.Reset()
	if code := Run([]string{"audit", "clear"}, td.deps); code != 0 {
		t.Fatalf("clear exit = %d: %s", code, td.stderr.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"audit", "-json"}, td.deps); code != 0 {
		t.Fatalf("audit after clear exit = %d: %s", code, td.stderr.String())
	}
	if strings.TrimSpace(td.stdout.String()) != "[]" {
		t.Fatalf("audit after clear = %q", td.stdout.String())
	}
}

func TestMacroRunWritesAuditEventsWithoutContent(t *testing.T) {
	td := newTestDeps()
	if err := td.metaStore.SetMacroDefinition("deploy", []byte(`{"version":1,"name":"deploy","steps":[{"kind":"send","send":"uptime\n"}]}`)); err != nil {
		t.Fatal(err)
	}
	td.deps.RunMacro = func(_ context.Context, host sshconfig.Host, _ []sshconfig.Host, macro automation.Macro, _, _ io.Writer, onEvent automation.EventFunc, _ bool) error {
		onEvent(automation.Event{Time: time.Now(), Type: automation.EventMacroStep, Macro: macro.Name, Step: "send-1", Message: "send"})
		onEvent(automation.Event{Time: time.Now(), Type: automation.EventMacroCompleted, Macro: macro.Name, Message: "1 steps completed"})
		return nil
	}
	if code := Run([]string{"macro", "run", "deploy", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("macro run exit = %d: %s", code, td.stderr.String())
	}
	events, err := td.metaStore.AuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	types := map[string]bool{}
	for _, event := range events {
		types[event.Type] = true
		if strings.Contains(event.Message, "uptime") {
			t.Fatalf("audit leaked command content: %#v", event)
		}
	}
	for _, want := range []string{"macro_started", "macro_step", "macro_completed"} {
		if !types[want] {
			t.Fatalf("missing %s in %#v", want, events)
		}
	}
}

func TestMacroRunWritesFailureAudit(t *testing.T) {
	td := newTestDeps()
	if err := td.metaStore.SetMacroDefinition("deploy", []byte(`{"version":1,"name":"deploy","steps":[{"kind":"send","send":"uptime\n"}]}`)); err != nil {
		t.Fatal(err)
	}
	td.deps.RunMacro = func(_ context.Context, _ sshconfig.Host, _ []sshconfig.Host, _ automation.Macro, _, _ io.Writer, _ automation.EventFunc, _ bool) error {
		return &sshconn.StageError{Stage: "automation", Host: "prod-db", Err: automation.ErrTimeout}
	}
	if code := Run([]string{"macro", "run", "deploy", "prod-db"}, td.deps); code != 1 {
		t.Fatalf("macro run exit = %d", code)
	}
	events, err := td.metaStore.AuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "macro_failed" || events[0].Message != "automation/automation_timeout" {
		t.Fatalf("events = %#v", events)
	}
}
