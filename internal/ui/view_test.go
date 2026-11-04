package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"whop/internal/sshconfig"
)

func TestListColumnsShowTagsAtMediumWidth(t *testing.T) {
	tests := []struct {
		width    int
		wantTags bool
	}{
		{width: 100, wantTags: true},
		{width: 80, wantTags: true},
		{width: 72, wantTags: true},
		{width: 60, wantTags: false},
	}
	for _, tt := range tests {
		if got := listColumns(tt.width).showTags; got != tt.wantTags {
			t.Errorf("listColumns(%d).showTags = %v, want %v", tt.width, got, tt.wantTags)
		}
	}
}

func TestViewListShowsTags(t *testing.T) {
	hosts := []sshconfig.Host{
		{Alias: "prod-db", HostName: "10.0.0.11", Port: 22, Tags: []string{"prod", "db"}, Kind: sshconfig.KindServer},
		{Alias: "bare", HostName: "10.0.0.12", Port: 22, Kind: sshconfig.KindServer},
	}
	m, _ := newTestModel(t, hosts)

	lines := m.viewList(100, 30)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "TAGS") {
		t.Fatalf("missing TAGS header:\n%s", joined)
	}
	if !strings.Contains(joined, "prod,db") {
		t.Fatalf("missing tags for prod-db:\n%s", joined)
	}
	if !strings.Contains(joined, "-") {
		t.Fatalf("missing placeholder for empty tags:\n%s", joined)
	}

	lines = m.viewList(72, 30)
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "TAGS") || !strings.Contains(joined, "prod,db") {
		t.Fatalf("tags missing at medium width:\n%s", joined)
	}
}

func TestSearchPlaceholderExplainsSlashShortcut(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	view := plainView(m)
	if !strings.Contains(view, "press / to search") {
		t.Fatalf("search placeholder should explain the shortcut:\n%s", view)
	}
	if strings.Contains(view, "type to filter") {
		t.Fatalf("search placeholder still claims direct typing works:\n%s", view)
	}
}

func TestNarrowFooterKeepsHelpAndQuitVisible(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.width = 40
	m.height = 20
	view := plainView(m)
	if !strings.Contains(view, "? help") || !strings.Contains(view, "q quit") {
		t.Fatalf("narrow footer hid essential keys:\n%s", view)
	}
}

func TestEmptyListStatesExplainNextAction(t *testing.T) {
	t.Run("no hosts", func(t *testing.T) {
		m, _ := newTestModel(t, nil)
		view := plainView(m)
		if !strings.Contains(view, "no hosts in") || !strings.Contains(view, "N create a connection") {
			t.Fatalf("empty config lacks next action:\n%s", view)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		m, _ := newTestModel(t, testHosts())
		sendKey(m, runeKey("/"))
		sendKey(m, runeKey("zzz"))
		view := plainView(m)
		if !strings.Contains(view, `no results for "zzz"`) || !strings.Contains(view, "ctrl+u clear search") {
			t.Fatalf("no-match state lacks next action:\n%s", view)
		}

		sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
		view = plainView(m)
		if !strings.Contains(view, "esc clear search") {
			t.Fatalf("active no-match state should explain esc:\n%s", view)
		}
	})

	t.Run("servers hidden", func(t *testing.T) {
		m, _ := newTestModel(t, []sshconfig.Host{{
			Alias: "github.com", HostName: "github.com", Kind: sshconfig.KindGitAuth,
		}})
		view := plainView(m)
		if !strings.Contains(view, "no servers found") || !strings.Contains(view, "a show all hosts") {
			t.Fatalf("hidden-server state lacks next action:\n%s", view)
		}
	})
}

func TestSecondaryScreensShowActionHintOnce(t *testing.T) {
	m, _ := newTestModel(t, testHosts())

	sendKey(m, runeKey("n"))
	if got := strings.Count(plainView(m), "ctrl+s save"); got != 1 {
		t.Fatalf("notes screen repeats action hint %d times:\n%s", got, plainView(m))
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})

	sendKey(m, runeKey("e"))
	if got := strings.Count(plainView(m), "tab/enter next"); got != 1 {
		t.Fatalf("edit screen repeats action hint %d times:\n%s", got, plainView(m))
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})

	m.diffMode = true
	m.diffTarget = testHosts()[0]
	m.diffLines = []string{"@@ -1 +1 @@", "-old", "+new"}
	if got := strings.Count(plainView(m), "enter/y apply"); got != 1 {
		t.Fatalf("diff screen repeats action hint %d times:\n%s", got, plainView(m))
	}
	m.diffMode = false

	m.macroMode = true
	m.macroTarget = testHosts()[0]
	m.macroNames = []string{"health"}
	if got := strings.Count(plainView(m), "enter run"); got != 1 {
		t.Fatalf("macro screen repeats action hint %d times:\n%s", got, plainView(m))
	}
}

func TestHelpDoesNotShowStaleListStatus(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.status = "edit cancelled"
	sendKey(m, runeKey("?"))
	view := plainView(m)
	if strings.Contains(view, "edit cancelled") {
		t.Fatalf("help screen showed stale status:\n%s", view)
	}
	if !strings.Contains(view, "? / esc close help") {
		t.Fatalf("help footer is not contextual:\n%s", view)
	}
}

func TestDetailPanelSkipsEmptyAndLowValueFields(t *testing.T) {
	m, _ := newTestModel(t, []sshconfig.Host{{
		Alias:    "minimal",
		HostName: "10.0.0.20",
		User:     "deploy",
		Port:     22,
		Kind:     sshconfig.KindServer,
		Source:   "~/.ssh/config",
	}})
	detail := plainView(m)
	for _, unwanted := range []string{"identity", "proxy", "tags", "kind", "favorite", "last", "count", "source"} {
		if strings.Contains(detail, unwanted) {
			t.Fatalf("minimal detail should omit %q:\n%s", unwanted, detail)
		}
	}

	full, _ := newTestModel(t, testHosts())
	for _, unwanted := range []string{"kind", "favorite", "last", "count", "source"} {
		if strings.Contains(plainView(full), unwanted) {
			t.Fatalf("detail should omit low-value field %q:\n%s", unwanted, plainView(full))
		}
	}
	for _, wanted := range []string{"identity", "tags"} {
		if !strings.Contains(plainView(full), wanted) {
			t.Fatalf("detail should keep useful field %q:\n%s", wanted, plainView(full))
		}
	}
}

func TestEditPasswordHintDependsOnMode(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("e"))
	if view := plainView(m); !strings.Contains(view, "Leave password blank to keep the current password") {
		t.Fatalf("edit password hint is not state-aware:\n%s", view)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})

	sendKey(m, runeKey("N"))
	if view := plainView(m); !strings.Contains(view, "Password optional; if set, saved in the encrypted vault") {
		t.Fatalf("create password hint is misleading:\n%s", view)
	}
}

func TestCompactModeLabelsReplaceBracketedBadges(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	view := plainView(m)
	for _, unwanted := range []string{"[servers]", "[FILTER EDIT]", "[FILTER ACTIVE]"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("view still contains noisy badge %q:\n%s", unwanted, view)
		}
	}

	sendKey(m, runeKey("/"))
	sendKey(m, runeKey("prod"))
	view = plainView(m)
	if !strings.Contains(view, "/ search: prod") || strings.Contains(view, "[FILTER EDIT]") {
		t.Fatalf("search state is not compact and clear:\n%s", view)
	}
}

func TestFooterStaysUsableAtSixtyColumns(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.width = 60
	m.height = 20
	view := plainView(m)
	for _, want := range []string{"enter connect", "/ search", "? help", "q quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("60-column footer missing %q:\n%s", want, view)
		}
	}
}

func TestHelpGroupsActionsAndExplainsFavorite(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("?"))
	view := plainView(m)
	for _, want := range []string{"NAVIGATION", "HOSTS", "SESSIONS", "favorite (*)", "up/down"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help missing %q:\n%s", want, view)
		}
	}
}

func TestDisplayConfigPathShortensHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	path := filepath.Join(home, ".ssh", "config")
	want := filepath.Join("~", ".ssh", "config")
	if got := displayConfigPath(path); got != want {
		t.Fatalf("displayConfigPath(%q) = %q, want %q", path, got, want)
	}
	if got := displayConfigPath("~/.ssh/config"); got != "~/.ssh/config" {
		t.Fatalf("already-short path changed to %q", got)
	}

	m, _ := newTestModel(t, testHosts())
	m.configPath = filepath.Join(home, "very", "long", "config")
	m.width = 60
	if view := plainView(m); strings.Contains(view, "very") {
		t.Fatalf("narrow header should omit the config path:\n%s", view)
	}
}
