package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"whop/internal/sshconfig"
)

func TestStatusResultExpiresAndIgnoresStaleTimer(t *testing.T) {
	var status statusState
	status.result("first", "prod-db")
	firstID := status.current.id
	status.result("second", "prod-db")

	status.expire(firstID)
	if got := status.text(); got != "second" {
		t.Fatalf("stale timer cleared newer notice: %q", got)
	}
	status.expire(status.current.id)
	if got := status.text(); got != "" {
		t.Fatalf("current result did not expire: %q", got)
	}
}

func TestStatusResultIsScopedButInitialNoticeStaysVisible(t *testing.T) {
	var status statusState
	status.result("REC on · prod-db", "prod-db")
	if _, ok := status.visible("prod-cache"); ok {
		t.Fatal("host-scoped result should be hidden for another selected host")
	}

	m := New(Options{
		ConfigPath: "~/.ssh/config",
		InitialNotice: Notice{
			Text:  "session ended · staging-db",
			Level: StatusResult,
			Scope: "staging-db",
		},
		Load: func() ([]sshconfig.Host, error) {
			return testHosts(), nil
		},
	})
	msg := m.Init()()
	m.Update(msg)
	if m.selectedAlias() == "staging-db" {
		t.Fatal("test requires the initial cursor to use a different host")
	}
	if !strings.Contains(plainView(m), "session ended · staging-db") {
		t.Fatalf("initial notice was hidden by its scope:\n%s", plainView(m))
	}
}

func TestRecordingNoticeClearsOnNavigation(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.setRecord = func(sshconfig.Host, bool) error { return nil }

	sendKey(m, runeKey("R"))
	if got := m.status.text(); got != "REC on · prod-db (next session)" {
		t.Fatalf("recording notice = %q", got)
	}
	if m.status.current.Level != StatusResult || m.status.current.Scope != "prod-db" {
		t.Fatalf("recording notice metadata = %+v", m.status.current)
	}

	sendKey(m, runeKey("j"))
	if got := m.status.text(); got != "" {
		t.Fatalf("recording notice survived host navigation: %q", got)
	}
	if m.selectedAlias() != "prod-cache" {
		t.Fatalf("selected host = %q", m.selectedAlias())
	}
}

func TestErrorNoticePersistsUntilDismissed(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.status.fail("connection failed · prod-db: boom", "prod-db")

	sendKey(m, runeKey("j"))
	if got := m.status.text(); got != "connection failed · prod-db: boom" {
		t.Fatalf("error should survive navigation, got %q", got)
	}
	if line := ansi.Strip(m.status.render(120, m.selectedAlias())); !strings.Contains(line, "! connection failed · prod-db: boom") {
		t.Fatalf("error line = %q", line)
	}

	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.status.text(); got != "" {
		t.Fatalf("esc did not dismiss error: %q", got)
	}
}

func TestStatusArmSchedulesResultExpiryOnce(t *testing.T) {
	var status statusState
	status.result("saved", "")
	if status.arm(nil) == nil {
		t.Fatal("result notice did not schedule an expiry")
	}
	if status.arm(nil) != nil {
		t.Fatal("result notice scheduled a duplicate expiry")
	}

	status.progress("loading...", "")
	if status.arm(nil) != nil {
		t.Fatal("progress notice must not expire")
	}
}

func TestNarrowSummaryIncludesRecordingState(t *testing.T) {
	hosts := testHosts()
	hosts[0].Record = true
	m, _ := newTestModel(t, hosts)
	m.width = 70
	m.height = 20
	if view := plainView(m); !strings.Contains(view, "recording on") {
		t.Fatalf("narrow summary missing recording state:\n%s", view)
	}
}
