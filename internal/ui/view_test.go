package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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

func TestListShowsUnifiedHostFlags(t *testing.T) {
	hosts := testHosts()
	hosts[0].Favorite = true
	hosts[1].Record = true
	m, _ := newTestModel(t, hosts)

	view := plainView(m)
	if !strings.Contains(view, "FLAGS") {
		t.Fatalf("flags column missing:\n%s", view)
	}
	favoriteRow := lineContaining(view, "prod-db")
	if !strings.Contains(favoriteRow, "*") || strings.Contains(favoriteRow, "*prod-db") {
		t.Fatalf("favorite should be a separate flag, got %q:\n%s", favoriteRow, view)
	}
	if recordingRow := lineContaining(view, "prod-cache"); !strings.Contains(recordingRow, "REC") {
		t.Fatalf("recording flag missing from row %q:\n%s", recordingRow, view)
	}
	if plainRow := lineContaining(view, "staging-db"); strings.Contains(plainRow, "*") || strings.Contains(plainRow, "REC") {
		t.Fatalf("host without flags should stay unmarked, got %q:\n%s", plainRow, view)
	}
	if !strings.Contains(view, "favorite  yes") {
		t.Fatalf("detail panel missing favorite status:\n%s", view)
	}
	sendKey(m, runeKey("j"))
	if view = plainView(m); !strings.Contains(view, "recording on") {
		t.Fatalf("detail panel missing recording status:\n%s", view)
	}
}

func TestCompactListUsesShortHostFlags(t *testing.T) {
	hosts := testHosts()
	hosts[0].Favorite = true
	hosts[1].Record = true
	m, _ := newTestModel(t, hosts)
	m.width = 40
	m.height = 20

	view := plainView(m)
	if !strings.Contains(view, "FLG") {
		t.Fatalf("compact flags header missing:\n%s", view)
	}
	if row := lineContaining(view, "prod-db"); !strings.Contains(row, "*") {
		t.Fatalf("favorite compact flag missing from row %q:\n%s", row, view)
	}
	if row := lineContaining(view, "prod-cache"); !strings.Contains(row, "R") {
		t.Fatalf("recording compact flag missing from row %q:\n%s", row, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("compact view line exceeds terminal width %d: %d\n%s", m.width, width, line)
		}
	}
}

func TestGlobalRecordingMarksEveryVisibleHost(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.record = true

	list := strings.Join(m.viewList(100, 30), "\n")
	for _, alias := range []string{"prod-db", "prod-cache", "staging-db"} {
		row := lineContaining(list, alias)
		if !strings.Contains(row, "REC") {
			t.Fatalf("global recording flag missing from %q row %q:\n%s", alias, row, list)
		}
	}
}

func TestRenderHostBadges(t *testing.T) {
	favorite := hostBadge{Label: "*", Compact: "*"}
	recording := hostBadge{Label: "REC", Compact: "R"}
	tests := []struct {
		name    string
		badges  []hostBadge
		compact bool
		limit   int
		want    string
	}{
		{name: "none", limit: 8, want: "-"},
		{name: "favorite", badges: []hostBadge{favorite}, limit: 8, want: "*"},
		{name: "recording", badges: []hostBadge{recording}, limit: 8, want: "REC"},
		{name: "combined", badges: []hostBadge{favorite, recording}, limit: 8, want: "* REC"},
		{name: "compact", badges: []hostBadge{favorite, recording}, compact: true, limit: 4, want: "* R"},
		{name: "overflow", badges: []hostBadge{{Label: "AAA"}, {Label: "BBB"}, {Label: "CCC"}}, limit: 3, want: "+2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderHostBadges(tt.badges, tt.compact, tt.limit); got != tt.want {
				t.Fatalf("renderHostBadges() = %q, want %q", got, tt.want)
			}
		})
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
	if !strings.Contains(view, "?: help") || !strings.Contains(view, "q: quit") {
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
		if !strings.Contains(view, "no login hosts found") || !strings.Contains(view, "a show all entries") {
			t.Fatalf("hidden-server state lacks next action:\n%s", view)
		}
	})
}

func TestSecondaryScreensShowActionHintOnce(t *testing.T) {
	m, _ := newTestModel(t, testHosts())

	sendKey(m, runeKey("n"))
	if got := strings.Count(plainView(m), "ctrl+s: save"); got != 1 {
		t.Fatalf("notes screen repeats action hint %d times:\n%s", got, plainView(m))
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})

	sendKey(m, runeKey("e"))
	if got := strings.Count(plainView(m), "tab/enter: next"); got != 1 {
		t.Fatalf("edit screen repeats action hint %d times:\n%s", got, plainView(m))
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})

	m.diffMode = true
	m.diffTarget = testHosts()[0]
	m.diffLines = []string{"@@ -1 +1 @@", "-old", "+new"}
	if got := strings.Count(plainView(m), "enter/y: apply"); got != 1 {
		t.Fatalf("diff screen repeats action hint %d times:\n%s", got, plainView(m))
	}
	m.diffMode = false

	m.macroMode = true
	m.macroTarget = testHosts()[0]
	m.macroNames = []string{"health"}
	if got := strings.Count(plainView(m), "enter: run"); got != 1 {
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
	if !strings.Contains(view, "?/esc: close help") {
		t.Fatalf("help footer is not contextual:\n%s", view)
	}
}

func TestSecondaryScreensHideListChrome(t *testing.T) {
	tests := []struct {
		name  string
		open  func(*Model)
		title string
	}{
		{name: "help", open: func(m *Model) { sendKey(m, runeKey("?")) }, title: "KEYBOARD"},
		{name: "edit", open: func(m *Model) { sendKey(m, runeKey("e")) }, title: "EDIT prod-db"},
		{name: "notes", open: func(m *Model) { sendKey(m, runeKey("n")) }, title: " NOTES prod-db"},
		{name: "forward", open: func(m *Model) {
			m.forwardMode = true
			m.forwardTarget = testHosts()[0]
			m.forwardType = newChoiceField("Type", []string{"local", "remote", "dynamic"}, 0)
			m.forwardFields = []textField{newTextField("Bind (local)", "", 20), newTextField("Destination (remote)", "", 20)}
		}, title: " FORWARD / prod-db"},
		{name: "macros", open: func(m *Model) {
			m.macroMode = true
			m.macroTarget = testHosts()[0]
			m.macroNames = []string{"health"}
		}, title: " MACROS / prod-db"},
		{name: "access", open: func(m *Model) {
			m.accessMode = true
			m.accessTarget = testHosts()[0]
			m.accessOptions = []sshconfig.AccessMode{sshconfig.AccessAuto, sshconfig.AccessLogin}
		}, title: " ACCESS / prod-db"},
		{name: "diff", open: func(m *Model) {
			m.diffMode = true
			m.diffTarget = testHosts()[0]
			m.diffLines = []string{"+new"}
		}, title: " CONFIG DIFF / prod-db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestModel(t, testHosts())
			tt.open(m)
			view := plainView(m)
			for _, unwanted := range []string{"3/4 hosts", "press / to search"} {
				if strings.Contains(view, unwanted) {
					t.Fatalf("%s screen still shows list chrome %q:\n%s", tt.name, unwanted, view)
				}
			}
			if !strings.Contains(view, tt.title) {
				t.Fatalf("%s screen missing contextual title %q:\n%s", tt.name, tt.title, view)
			}
		})
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
	for _, want := range []string{"enter: connect", "/: search", "?: help", "q: quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("60-column footer missing %q:\n%s", want, view)
		}
	}
}

func TestHelpGroupsActionsAndExplainsFavorite(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("?"))
	view := plainView(m)
	for _, want := range []string{"NAVIGATION", "HOSTS", "SESSIONS", "favorite (*)", "up/down", "set login access"} {
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

func goldenView(m *Model) []byte {
	lines := strings.Split(plainView(m), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	return []byte(strings.Join(lines, "\n"))
}

func TestMainScreenGolden(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	got := goldenView(m)
	path := filepath.Join("testdata", "main-screen.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run UPDATE_GOLDEN=1 go test ./internal/ui -run TestMainScreenGolden): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("main screen changed\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestFlaggedMainScreenGolden(t *testing.T) {
	hosts := testHosts()
	hosts[0].Favorite = true
	hosts[1].Record = true
	m, _ := newTestModel(t, hosts)
	got := goldenView(m)
	path := filepath.Join("testdata", "flagged-main-screen.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run UPDATE_GOLDEN=1 go test ./internal/ui -run TestFlaggedMainScreenGolden): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("flagged main screen changed\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}
