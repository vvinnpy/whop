package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"ttydeck/internal/sshconfig"
)

func testHosts() []sshconfig.Host {
	return []sshconfig.Host{
		{
			Alias:         "prod-db",
			HostName:      "10.0.0.11",
			User:          "deploy",
			Port:          22,
			IdentityFiles: []string{"~/.ssh/id_ed25519"},
			Tags:          []string{"prod", "db"},
			Kind:          sshconfig.KindServer,
			Source:        "~/.ssh/config",
		},
		{
			Alias:    "prod-cache",
			HostName: "10.0.0.12",
			User:     "deploy",
			Port:     2222,
			Tags:     []string{"prod"},
			Kind:     sshconfig.KindServer,
			Source:   "~/.ssh/config",
		},
		{
			Alias:    "staging-db",
			HostName: "10.1.0.11",
			User:     "admin",
			Port:     22,
			Tags:     []string{"staging", "db"},
			Kind:     sshconfig.KindServer,
			Source:   "~/.ssh/config",
		},
		{
			Alias:    "github.com",
			HostName: "github.com",
			User:     "git",
			Port:     22,
			Kind:     sshconfig.KindGitAuth,
			Source:   "~/.ssh/config",
		},
	}
}

func newTestModel(t *testing.T, hosts []sshconfig.Host) (*Model, *string) {
	t.Helper()
	connected := ""
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(host sshconfig.Host, _ bool) tea.Cmd {
			connected = host.Alias
			return nil
		},
	})
	msg := m.Init()()
	if msg != nil {
		m.Update(msg)
	}
	return m, &connected
}

func plainView(m *Model) string {
	return ansi.Strip(m.View())
}

func sendKey(m *Model, key tea.KeyMsg) tea.Cmd {
	_, cmd := m.Update(key)
	return cmd
}

func runeKey(value string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}

func TestInitialViewShowsServers(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	view := plainView(m)

	for _, want := range []string{"ttydeck", "3/4 hosts", "prod-db", "10.0.0.11", "deploy", "[servers]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "github.com") {
		t.Fatalf("git auth host must be hidden by default:\n%s", view)
	}
}

func TestFilterFocusAndType(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("/"))
	if !m.filterFocused {
		t.Fatal("filter should be focused")
	}
	sendKey(m, runeKey("prod db"))
	if m.filter != "prod db" {
		t.Fatalf("filter = %q", m.filter)
	}
	if len(m.filtered) != 1 || m.filtered[0].Alias != "prod-db" {
		t.Fatalf("unexpected filtered: %#v", m.filtered)
	}
	if !strings.Contains(plainView(m), "prod-db") {
		t.Fatal("filtered view missing prod-db")
	}
}

func TestFilterBackspaceAndEscape(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("/"))
	sendKey(m, runeKey("prod"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filter != "pro" {
		t.Fatalf("filter = %q", m.filter)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.filter != "" || m.filterFocused {
		t.Fatalf("filter state not cleared: %q focused=%v", m.filter, m.filterFocused)
	}
	if len(m.filtered) != 3 {
		t.Fatalf("expected all servers after clear, got %d", len(m.filtered))
	}
}

func TestNavigation(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("j"))
	if m.cursor != 1 {
		t.Fatalf("cursor after j = %d", m.cursor)
	}
	sendKey(m, runeKey("k"))
	if m.cursor != 0 {
		t.Fatalf("cursor after k = %d", m.cursor)
	}
	sendKey(m, runeKey("G"))
	if m.cursor != 2 {
		t.Fatalf("cursor after G = %d", m.cursor)
	}
	sendKey(m, runeKey("g"))
	if m.cursor != 0 {
		t.Fatalf("cursor after g = %d", m.cursor)
	}
}

func TestEnterConnectsSelectedHost(t *testing.T) {
	m, connected := newTestModel(t, testHosts())
	sendKey(m, runeKey("j"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if *connected != "prod-cache" {
		t.Fatalf("connected = %q", *connected)
	}
	if !strings.Contains(m.status, "connecting to prod-cache") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestToggleAllIncludesGitAuth(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("a"))
	if !m.showAll || len(m.filtered) != 4 {
		t.Fatalf("showAll=%v filtered=%d", m.showAll, len(m.filtered))
	}
	if !strings.Contains(plainView(m), "github.com") {
		t.Fatal("all mode should show git auth host")
	}
	sendKey(m, runeKey("a"))
	if m.showAll || len(m.filtered) != 3 {
		t.Fatalf("showAll=%v filtered=%d", m.showAll, len(m.filtered))
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("?"))
	if !m.help {
		t.Fatal("help should be open")
	}
	if !strings.Contains(plainView(m), "KEYBOARD") {
		t.Fatal("help content missing")
	}
	sendKey(m, runeKey("?"))
	if m.help {
		t.Fatal("help should be closed")
	}
}

func TestWideAndNarrowLayouts(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	wide := plainView(m)
	if !strings.Contains(wide, "identity") {
		t.Fatalf("wide layout should show detail panel:\n%s", wide)
	}

	m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	narrow := plainView(m)
	if strings.Contains(narrow, "identity") {
		t.Fatalf("narrow layout should hide detail panel:\n%s", narrow)
	}
	if !strings.Contains(narrow, "prod-db") || !strings.Contains(narrow, "10.0.0.11:22") {
		t.Fatalf("narrow layout missing summary:\n%s", narrow)
	}
}

func TestEscQuitsWhenFilterEmpty(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	msg := cmd()
	if fmt.Sprintf("%T", msg) != "tea.QuitMsg" {
		t.Fatalf("unexpected quit message: %T", msg)
	}
}

func TestReload(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	cmd := sendKey(m, runeKey("r"))
	if cmd == nil {
		t.Fatal("expected reload command")
	}
	msg := cmd()
	if _, ok := msg.(hostsLoadedMsg); !ok {
		t.Fatalf("unexpected reload message: %T", msg)
	}
	m.Update(msg)
	if len(m.allHosts) != 4 {
		t.Fatalf("hosts after reload = %d", len(m.allHosts))
	}
}

func TestLoadError(t *testing.T) {
	m := New(Options{
		Load: func() ([]sshconfig.Host, error) {
			return nil, fmt.Errorf("boom")
		},
	})
	msg := m.Init()()
	m.Update(msg)
	if !strings.Contains(plainView(m), "boom") {
		t.Fatal("error not shown")
	}
}

func TestViewFitsCommonWidths(t *testing.T) {
	for _, width := range []int{60, 80, 96, 120} {
		m, _ := newTestModel(t, testHosts())
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		for _, line := range strings.Split(plainView(m), "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d: line width %d: %q", width, got, line)
			}
		}
	}
}

func TestToggleFavorite(t *testing.T) {
	hosts := testHosts()
	var toggledAlias string
	var toggledValue bool
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		ToggleFavorite: func(host sshconfig.Host, favorite bool) error {
			toggledAlias = host.Alias
			toggledValue = favorite
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("f"))
	if toggledAlias != "prod-db" || !toggledValue {
		t.Fatalf("favorite callback = %q %v", toggledAlias, toggledValue)
	}
	if !m.allHosts[0].Favorite {
		t.Fatal("host favorite flag not updated")
	}
	if !strings.Contains(plainView(m), "*prod-db") {
		t.Fatalf("favorite marker missing:\n%s", plainView(m))
	}
}

func TestRecordToggle(t *testing.T) {
	hosts := testHosts()
	var recorded bool
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(_ sshconfig.Host, record bool) tea.Cmd {
			recorded = record
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("R"))
	if !m.record {
		t.Fatal("recording should be enabled")
	}
	if !strings.Contains(plainView(m), "rec") {
		t.Fatalf("recording badge missing:\n%s", plainView(m))
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !recorded {
		t.Fatal("connect did not receive recording flag")
	}
}
