package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
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

	for _, want := range []string{"whop", "3/4 hosts", "prod-db", "10.0.0.11", "deploy", "login hosts"} {
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
	if view := plainView(m); !strings.Contains(view, "/ search:") || !strings.Contains(view, "prod-db") {
		t.Fatalf("filtered view missing edit state or host:\n%s", view)
	}
}

func TestFilterBackspaceAndEscapeAppliesFilter(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("/"))
	sendKey(m, runeKey("prod"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filter != "pro" {
		t.Fatalf("filter = %q", m.filter)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.filter != "pro" || m.filterFocused {
		t.Fatalf("filter state not applied: %q focused=%v", m.filter, m.filterFocused)
	}
	if len(m.filtered) != 2 {
		t.Fatalf("expected filtered servers after esc, got %d", len(m.filtered))
	}
	if view := plainView(m); !strings.Contains(view, "/ search:") {
		t.Fatalf("active filter indicator missing:\n%s", view)
	}
	sendKey(m, runeKey("e"))
	if !m.editing || m.editTarget.Alias != "prod-db" {
		t.Fatalf("edit did not start on filtered host: editing=%v target=%+v", m.editing, m.editTarget)
	}
}

func TestFilterTabAppliesWithoutClearing(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("/"))
	sendKey(m, runeKey("prod"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.filter != "prod" || m.filterFocused {
		t.Fatalf("filter = %q focused=%v", m.filter, m.filterFocused)
	}
	if len(m.filtered) != 2 {
		t.Fatalf("expected two filtered servers, got %d", len(m.filtered))
	}
}

func TestEscapeClearsActiveFilterWithoutQuitting(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("/"))
	sendKey(m, runeKey("prod"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.filter != "" || len(m.filtered) != 3 {
		t.Fatalf("active filter not cleared: %q filtered=%d", m.filter, len(m.filtered))
	}
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("esc must not quit after clearing the filter")
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
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.cursor != 1 {
		t.Fatalf("cursor after ctrl+n = %d", m.cursor)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.cursor != 0 {
		t.Fatalf("cursor after ctrl+p = %d", m.cursor)
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

func TestCtrlNavigationInsideFilter(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("/"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.filter != "" || m.cursor != 1 {
		t.Fatalf("ctrl+n filter=%q cursor=%d", m.filter, m.cursor)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.filter != "" || m.cursor != 0 {
		t.Fatalf("ctrl+p filter=%q cursor=%d", m.filter, m.cursor)
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
	if view := plainView(m); !strings.Contains(view, "all entries") || !strings.Contains(view, "ACCESS") {
		t.Fatalf("all mode should show mode and access column:\n%s", view)
	}
	sendKey(m, runeKey("G"))
	if view := plainView(m); !strings.Contains(view, "service (auto)") {
		t.Fatalf("all mode should explain automatic service access:\n%s", view)
	}
	sendKey(m, runeKey("a"))
	if m.showAll || len(m.filtered) != 3 {
		t.Fatalf("showAll=%v filtered=%d", m.showAll, len(m.filtered))
	}
}

func TestAccessPickerSetsServiceAndHidesHost(t *testing.T) {
	hosts := testHosts()
	var selectedAlias string
	var selectedMode sshconfig.AccessMode
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		SetLoginMode: func(host sshconfig.Host, mode sshconfig.AccessMode) error {
			selectedAlias = host.Alias
			selectedMode = mode
			for i := range hosts {
				if hosts[i].Alias == host.Alias {
					hosts[i].AccessMode = mode
				}
			}
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("L"))
	if !m.accessMode {
		t.Fatal("access picker should open")
	}
	if view := plainView(m); !strings.Contains(view, "ACCESS / prod-db") || !strings.Contains(view, "service endpoint") {
		t.Fatalf("access picker missing choices:\n%s", view)
	}
	sendKey(m, runeKey("j"))
	sendKey(m, runeKey("j"))
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("access selection should reload hosts")
	}
	if selectedAlias != "prod-db" || selectedMode != sshconfig.AccessService {
		t.Fatalf("access callback = %q %q", selectedAlias, selectedMode)
	}
	m.Update(cmd())
	if m.accessMode {
		t.Fatal("access picker should close after apply")
	}
	if len(m.filtered) != 2 {
		t.Fatalf("service host should leave login list, got %d hosts", len(m.filtered))
	}
	if !strings.Contains(m.status, "hidden; press a to show all") {
		t.Fatalf("status should explain hidden host: %q", m.status)
	}

	sendKey(m, runeKey("a"))
	if len(m.filtered) != 4 {
		t.Fatalf("all entries should include the service host, got %d", len(m.filtered))
	}
	sendKey(m, runeKey("g"))
	if view := plainView(m); !strings.Contains(view, "service (manual)") {
		t.Fatalf("manual service override should be visible:\n%s", view)
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("?"))
	if !m.help {
		t.Fatal("help should be open")
	}
	if view := plainView(m); !strings.Contains(view, "KEYBOARD") || !strings.Contains(view, "NAVIGATION") || !strings.Contains(view, "up/down") {
		t.Fatalf("help content missing navigation keys:\n%s", view)
	} else if !strings.Contains(view, "copy selected host") {
		t.Fatalf("help content missing copy action:\n%s", view)
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

func TestEscDoesNotQuitWhenFilterEmpty(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("esc must not quit; q remains the quit key")
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
	var persisted bool
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(_ sshconfig.Host, record bool) tea.Cmd {
			recorded = record
			return nil
		},
		SetRecord: func(_ sshconfig.Host, enabled bool) error {
			persisted = enabled
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("R"))
	if !persisted || !m.allHosts[0].Record {
		t.Fatal("recording policy should be enabled for the selected host")
	}
	if !strings.Contains(plainView(m), "rec") {
		t.Fatalf("recording badge missing:\n%s", plainView(m))
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !recorded {
		t.Fatal("connect did not receive recording flag")
	}
}

func TestEditFormSave(t *testing.T) {
	hosts := testHosts()
	var edited sshconfig.Edit
	var editedTags []string
	var savedPassword string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		EditHost: func(_ sshconfig.Host, edit sshconfig.Edit) error {
			edited = edit
			return nil
		},
		SetHostPassword: func(_ sshconfig.Host, password string) error {
			savedPassword = password
			return nil
		},
		SetTags: func(_ sshconfig.Host, tags []string) error {
			editedTags = tags
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("e"))
	if !m.editing {
		t.Fatal("edit mode should be active")
	}
	if !strings.Contains(plainView(m), "EDIT prod-db") {
		t.Fatalf("edit view missing title:\n%s", plainView(m))
	}
	m.editFields[editFieldHostName].SetValue("10.0.0.99")
	m.editFields[editFieldUser].SetValue("admin")
	m.editFields[editFieldPort].SetValue("2222")
	m.editFields[editFieldProxyJump].SetValue("bastion")
	m.editFields[editFieldIdentityFile].SetValue("~/.ssh/id_work")
	m.editFields[editFieldTags].SetValue("prod,db")
	m.editFields[editFieldPassword].SetValue("new-secret")
	m.editFields[editFieldPasswordConfirm].SetValue("new-secret")
	if view := plainView(m); strings.Contains(view, "new-secret") {
		t.Fatalf("password leaked in edit view:\n%s", view)
	}
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.editing {
		t.Fatal("edit mode should close after save")
	}
	if cmd == nil {
		t.Fatal("save should trigger reload")
	}
	if edited.HostName == nil || *edited.HostName != "10.0.0.99" {
		t.Fatalf("hostname not edited: %+v", edited)
	}
	if edited.Port == nil || *edited.Port != 2222 {
		t.Fatalf("port not edited: %+v", edited)
	}
	if edited.IdentityFile == nil || *edited.IdentityFile != "~/.ssh/id_work" {
		t.Fatalf("identity file not edited: %+v", edited)
	}
	if savedPassword != "new-secret" {
		t.Fatalf("saved password = %q", savedPassword)
	}
	if m.editFields[editFieldPassword].Value() != "" {
		t.Fatal("password remained in edit form after save")
	}
	if len(editedTags) != 2 || editedTags[0] != "prod" || editedTags[1] != "db" {
		t.Fatalf("tags = %#v", editedTags)
	}
}

func TestEditPasswordSaveFailureKeepsConfigUntouched(t *testing.T) {
	hosts := testHosts()
	editCalled := false
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		EditHost: func(sshconfig.Host, sshconfig.Edit) error {
			editCalled = true
			return nil
		},
		SetHostPassword: func(sshconfig.Host, string) error {
			return fmt.Errorf("vault locked")
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("e"))
	m.editFields[editFieldPassword].SetValue("new-secret")
	m.editFields[editFieldPasswordConfirm].SetValue("new-secret")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.editing {
		t.Fatal("edit form should remain open after password failure")
	}
	if editCalled {
		t.Fatal("config edit ran after password storage failed")
	}
	if m.editFields[editFieldPassword].Value() != "new-secret" {
		t.Fatal("password should remain available for retry")
	}
	if !strings.Contains(plainView(m), "vault locked") {
		t.Fatalf("password error missing:\n%s", plainView(m))
	}
}

func TestEditPasswordWaitsForDiffConfirmation(t *testing.T) {
	hosts := testHosts()
	var savedPassword string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		PreviewEdit: func(sshconfig.Host, sshconfig.Edit) (sshconfig.Preview, error) {
			return sshconfig.Preview{Diff: "--- a/config\n+++ b/config\n@@ -1 +1 @@\n-old\n+new\n", Hash: "preview-hash"}, nil
		},
		EditHost: func(sshconfig.Host, sshconfig.Edit) error { return nil },
		SetHostPassword: func(_ sshconfig.Host, password string) error {
			savedPassword = password
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("e"))
	m.editFields[editFieldHostName].SetValue("10.0.0.99")
	m.editFields[editFieldPassword].SetValue("new-secret")
	m.editFields[editFieldPasswordConfirm].SetValue("new-secret")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.diffMode {
		t.Fatal("expected diff confirmation")
	}
	if savedPassword != "" {
		t.Fatal("password was saved before diff confirmation")
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if savedPassword != "new-secret" {
		t.Fatalf("saved password = %q", savedPassword)
	}
}

func TestEditFormRejectsPasswordMismatch(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("e"))
	m.editFields[editFieldPassword].SetValue("new-secret")
	m.editFields[editFieldPasswordConfirm].SetValue("typo")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.editing {
		t.Fatal("edit mode should remain active")
	}
	if !strings.Contains(plainView(m), "passwords do not match") {
		t.Fatalf("mismatch not shown:\n%s", plainView(m))
	}
	if m.editFocus != editFieldPasswordConfirm {
		t.Fatalf("expected confirmation field focus, got %d", m.editFocus)
	}
}

func TestEditFormRejectsInvalidPort(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	sendKey(m, runeKey("e"))
	m.editFields[editFieldPort].SetValue("not-a-port")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.editing {
		t.Fatal("edit mode should remain active")
	}
	if !strings.Contains(plainView(m), "invalid port") {
		t.Fatalf("invalid port not shown:\n%s", plainView(m))
	}
	if m.editFocus != editFieldPort {
		t.Fatalf("expected port field focus, got %d", m.editFocus)
	}
}

func TestNotesEditorSave(t *testing.T) {
	hosts := testHosts()
	var savedAlias, savedNote string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		SaveNotes: func(host sshconfig.Host, note string) error {
			savedAlias = host.Alias
			savedNote = note
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("n"))
	if !m.notesMode {
		t.Fatal("notes mode should be active")
	}
	m.notesInput.SetValue("primary database")
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.notesMode {
		t.Fatal("notes mode should close after save")
	}
	if cmd == nil {
		t.Fatal("notes save should reload")
	}
	if savedAlias != "prod-db" || savedNote != "primary database" {
		t.Fatalf("saved = %q %q", savedAlias, savedNote)
	}
}

func TestMacroPickerRunsSelectedMacro(t *testing.T) {
	hosts := testHosts()
	var ranHost, ranName string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		LoadMacros: func() ([]string, error) {
			return []string{"collect", "restart"}, nil
		},
		RunMacro: func(host sshconfig.Host, name string) tea.Cmd {
			ranHost, ranName = host.Alias, name
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("m"))
	if !m.macroMode || len(m.macroNames) != 2 {
		t.Fatalf("macro mode = %v names=%#v", m.macroMode, m.macroNames)
	}
	if !strings.Contains(plainView(m), "MACROS / prod-db") || !strings.Contains(plainView(m), "collect") {
		t.Fatalf("macro view:\n%s", plainView(m))
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.macroMode {
		t.Fatal("macro picker should close after run")
	}
	if ranHost != "prod-db" || ranName != "restart" {
		t.Fatalf("ran macro host=%q name=%q", ranHost, ranName)
	}
	if !strings.Contains(m.status, "running macro restart") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestMacroFinishedStatus(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.Update(MacroFinishedMsg{Host: "prod-db", Name: "collect"})
	if !strings.Contains(m.status, "macro collect completed on prod-db") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestEditShowsDiffBeforeApplying(t *testing.T) {
	hosts := testHosts()
	var previewed sshconfig.Edit
	var applied sshconfig.Edit
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		PreviewEdit: func(_ sshconfig.Host, edit sshconfig.Edit) (sshconfig.Preview, error) {
			previewed = edit
			return sshconfig.Preview{Diff: "--- a/config\n+++ b/config\n@@ -1,2 +1,2 @@\n-HostName 10.0.0.11\n+HostName 10.0.0.99\n", Hash: "preview-hash"}, nil
		},
		EditHost: func(_ sshconfig.Host, edit sshconfig.Edit) error {
			applied = edit
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("e"))
	m.editFields[editFieldHostName].SetValue("10.0.0.99")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.diffMode || m.editing {
		t.Fatalf("diff mode=%v editing=%v", m.diffMode, m.editing)
	}
	if previewed.HostName == nil || *previewed.HostName != "10.0.0.99" {
		t.Fatalf("preview edit = %+v", previewed)
	}
	if applied.HostName != nil {
		t.Fatal("edit was applied before confirmation")
	}
	if view := plainView(m); !strings.Contains(view, "CONFIG DIFF / prod-db") || !strings.Contains(view, "+HostName 10.0.0.99") {
		t.Fatalf("diff view missing changes:\n%s", view)
	}
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("apply should reload hosts")
	}
	if m.diffMode || applied.HostName == nil || *applied.HostName != "10.0.0.99" || applied.ExpectedHash != "preview-hash" {
		t.Fatalf("diffMode=%v applied=%+v", m.diffMode, applied)
	}
}

func TestEditDiffCanReturnToForm(t *testing.T) {
	hosts := testHosts()
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		PreviewEdit: func(sshconfig.Host, sshconfig.Edit) (sshconfig.Preview, error) {
			return sshconfig.Preview{Diff: "--- a/config\n+++ b/config\n@@ -1 +1 @@\n-old\n+new\n", Hash: "preview-hash"}, nil
		},
		EditHost: func(sshconfig.Host, sshconfig.Edit) error { return nil },
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("e"))
	m.editFields[editFieldUser].SetValue("admin")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.diffMode || !m.editing {
		t.Fatalf("diffMode=%v editing=%v", m.diffMode, m.editing)
	}
	if got := m.editFields[editFieldUser].Value(); got != "admin" {
		t.Fatalf("edit field was not preserved: %q", got)
	}
}

func TestEditNoDiffAppliesImmediately(t *testing.T) {
	hosts := testHosts()
	var applied sshconfig.Edit
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		PreviewEdit: func(sshconfig.Host, sshconfig.Edit) (sshconfig.Preview, error) {
			return sshconfig.Preview{}, nil
		},
		EditHost: func(_ sshconfig.Host, edit sshconfig.Edit) error {
			applied = edit
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("e"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.diffMode || applied.HostName != nil || applied.User != nil || applied.Port != nil {
		t.Fatalf("diffMode=%v applied=%+v", m.diffMode, applied)
	}
}

func TestInitialStatusSurvivesHostLoad(t *testing.T) {
	m := New(Options{
		ConfigPath:    "~/.ssh/config",
		InitialStatus: "session ended",
		Load: func() ([]sshconfig.Host, error) {
			return testHosts(), nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
	})
	msg := m.Init()()
	m.Update(msg)
	if m.status != "session ended" {
		t.Fatalf("status = %q", m.status)
	}
	if !strings.Contains(plainView(m), "session ended") {
		t.Fatalf("status not rendered:\n%s", plainView(m))
	}
}

func TestEditFormRenamesHostAndMigratesMetadata(t *testing.T) {
	hosts := testHosts()
	var edited sshconfig.Edit
	var renamedFrom, renamedTo string
	var taggedAlias string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		PreviewEdit: func(sshconfig.Host, sshconfig.Edit) (sshconfig.Preview, error) {
			return sshconfig.Preview{}, nil
		},
		EditHost: func(_ sshconfig.Host, edit sshconfig.Edit) error {
			edited = edit
			return nil
		},
		RenameHost: func(oldAlias, newAlias string) error {
			renamedFrom, renamedTo = oldAlias, newAlias
			return nil
		},
		SetTags: func(host sshconfig.Host, _ []string) error {
			taggedAlias = host.Alias
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("e"))
	m.editFields[editFieldAlias].SetValue("prod-main")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if edited.Alias == nil || *edited.Alias != "prod-main" {
		t.Fatalf("alias edit = %+v", edited)
	}
	if renamedFrom != "prod-db" || renamedTo != "prod-main" {
		t.Fatalf("rename = %q -> %q", renamedFrom, renamedTo)
	}
	if taggedAlias != "prod-main" {
		t.Fatalf("tags applied to %q", taggedAlias)
	}
}

func TestCreateHostFlow(t *testing.T) {
	hosts := testHosts()
	var previewed sshconfig.HostImport
	var added sshconfig.HostImport
	var addedHash string
	var taggedAlias, savedPassword string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		PreviewAdd: func(imp sshconfig.HostImport) (sshconfig.Preview, error) {
			previewed = imp
			return sshconfig.Preview{Diff: "--- config\n+++ config\n@@ -0,0 +1,2 @@\n+Host lab\n+  HostName 10.0.0.50\n", Hash: "preview-hash"}, nil
		},
		AddHost: func(imp sshconfig.HostImport, expectedHash string) error {
			added = imp
			addedHash = expectedHash
			return nil
		},
		SetTags: func(host sshconfig.Host, _ []string) error {
			taggedAlias = host.Alias
			return nil
		},
		SetHostPassword: func(_ sshconfig.Host, password string) error {
			savedPassword = password
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("N"))
	if !m.creating || !m.editing {
		t.Fatal("create form should be active")
	}
	if !strings.Contains(plainView(m), "NEW HOST") {
		t.Fatalf("create view title missing:\n%s", plainView(m))
	}
	m.editFields[editFieldAlias].SetValue("lab")
	m.editFields[editFieldHostName].SetValue("10.0.0.50")
	m.editFields[editFieldUser].SetValue("admin")
	m.editFields[editFieldPort].SetValue("2222")
	m.editFields[editFieldProxyJump].SetValue("bastion")
	m.editFields[editFieldTags].SetValue("lab,test")
	m.editFields[editFieldPassword].SetValue("secret")
	m.editFields[editFieldPasswordConfirm].SetValue("secret")
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.diffMode || added.Alias != "" {
		t.Fatalf("diffMode=%v added=%+v", m.diffMode, added)
	}
	if previewed.Alias != "lab" || previewed.IdentityFile != "" {
		t.Fatalf("preview = %+v", previewed)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if added.Alias != "lab" || added.HostName != "10.0.0.50" || added.User != "admin" || added.Port != 2222 || added.ProxyJump != "bastion" {
		t.Fatalf("added = %+v", added)
	}
	if addedHash != "preview-hash" {
		t.Fatalf("hash = %q", addedHash)
	}
	if taggedAlias != "lab" || savedPassword != "secret" {
		t.Fatalf("tags=%q password=%q", taggedAlias, savedPassword)
	}
	if m.editing || m.creating {
		t.Fatal("create form should close")
	}
}

func TestCopyHostFlow(t *testing.T) {
	hosts := append(testHosts(), sshconfig.Host{
		Alias:    "prod-db-copy",
		HostName: "10.0.0.99",
		Port:     22,
	})
	var previewed sshconfig.HostImport
	var added sshconfig.HostImport
	var tagged []string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		PreviewAdd: func(imp sshconfig.HostImport) (sshconfig.Preview, error) {
			previewed = imp
			return sshconfig.Preview{Diff: "--- config\n+++ config\n@@ -0,0 +1,2 @@\n+Host " + imp.Alias + "\n+  HostName " + imp.HostName + "\n", Hash: "preview-hash"}, nil
		},
		AddHost: func(imp sshconfig.HostImport, expectedHash string) error {
			added = imp
			if expectedHash != "preview-hash" {
				t.Fatalf("expected hash = %q", expectedHash)
			}
			return nil
		},
		SetTags: func(_ sshconfig.Host, tags []string) error {
			tagged = append([]string(nil), tags...)
			return nil
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("c"))
	if !m.creating || !m.copying || !m.editing {
		t.Fatalf("copy form should be active: creating=%v copying=%v editing=%v", m.creating, m.copying, m.editing)
	}
	if got := m.editFields[editFieldAlias].Value(); got != "prod-db-copy-2" {
		t.Fatalf("copy alias = %q", got)
	}
	if got := m.editFields[editFieldHostName].Value(); got != "10.0.0.11" {
		t.Fatalf("copy hostname = %q", got)
	}
	if got := m.editFields[editFieldUser].Value(); got != "deploy" {
		t.Fatalf("copy user = %q", got)
	}
	if got := m.editFields[editFieldPort].Value(); got != "22" {
		t.Fatalf("copy port = %q", got)
	}
	if got := m.editFields[editFieldIdentityFile].Value(); got != "~/.ssh/id_ed25519" {
		t.Fatalf("copy identity = %q", got)
	}
	if got := m.editFields[editFieldTags].Value(); got != "prod,db" {
		t.Fatalf("copy tags = %q", got)
	}
	if got := m.editFields[editFieldPassword].Value(); got != "" {
		t.Fatalf("copy must not carry a password: %q", got)
	}
	if view := plainView(m); !strings.Contains(view, "COPY / prod-db") || strings.Contains(view, "NEW HOST") {
		t.Fatalf("copy title missing:\n%s", view)
	}

	sendKey(m, runeKey("lab-02"))
	if got := m.editFields[editFieldAlias].Value(); got != "lab-02" {
		t.Fatalf("selected alias was not replaced: %q", got)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	sendKey(m, runeKey("10.0.0.50"))
	if got := m.editFields[editFieldHostName].Value(); got != "10.0.0.50" {
		t.Fatalf("selected hostname was not replaced: %q", got)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.diffMode || previewed.Alias != "lab-02" || previewed.HostName != "10.0.0.50" {
		t.Fatalf("diffMode=%v preview=%+v", m.diffMode, previewed)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if added.Alias != "lab-02" || added.HostName != "10.0.0.50" || added.User != "deploy" || added.Port != 22 || added.IdentityFile != "~/.ssh/id_ed25519" {
		t.Fatalf("added = %+v", added)
	}
	if strings.Join(tagged, ",") != "prod,db" {
		t.Fatalf("tags = %#v", tagged)
	}
	if m.copying || m.creating || m.editing || m.status != "host copied" {
		t.Fatalf("copy state not cleared: copying=%v creating=%v editing=%v status=%q", m.copying, m.creating, m.editing, m.status)
	}
}

func TestCopyHostWarnsForMultipleIdentityFiles(t *testing.T) {
	hosts := []sshconfig.Host{{
		Alias:         "dual-key",
		HostName:      "10.0.0.20",
		Port:          22,
		IdentityFiles: []string{"~/.ssh/id_one", "~/.ssh/id_two"},
		Kind:          sshconfig.KindServer,
	}}
	m, _ := newTestModel(t, hosts)
	sendKey(m, runeKey("c"))
	if got := m.editFields[editFieldIdentityFile].Value(); got != "" {
		t.Fatalf("ambiguous identities should not be copied, got %q", got)
	}
	if !strings.Contains(m.status, "multiple identity files need manual review") {
		t.Fatalf("warning missing: %q", m.status)
	}
}

func TestLoadedStateHasNoDuplicateHostCount(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	if m.status != "" {
		t.Fatalf("default status should be empty, got %q", m.status)
	}
	if view := plainView(m); strings.Contains(view, "servers / 4 total") {
		t.Fatalf("host count is duplicated in the status line:\n%s", view)
	}
}

func TestFilterChangesClearTransientStatus(t *testing.T) {
	m, _ := newTestModel(t, testHosts())
	m.status = "session ended"
	sendKey(m, runeKey("/"))
	sendKey(m, runeKey("p"))
	if m.status != "" {
		t.Fatalf("typing in search left stale status %q", m.status)
	}

	m.status = "session ended"
	sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.status != "" {
		t.Fatalf("applying search left stale status %q", m.status)
	}

	m.status = "session ended"
	sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.filter != "" || m.status != "" {
		t.Fatalf("clearing search filter=%q status=%q", m.filter, m.status)
	}
}

func TestEditEnterAdvancesFieldsAndLastFieldSaves(t *testing.T) {
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return testHosts(), nil
		},
		Connect:  func(sshconfig.Host, bool) tea.Cmd { return nil },
		EditHost: func(sshconfig.Host, sshconfig.Edit) error { return nil },
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("e"))

	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.editing || m.editFocus != editFieldHostName {
		t.Fatalf("enter should advance to the next field: editing=%v focus=%d", m.editing, m.editFocus)
	}

	m.focusEditFieldIndex(editFieldPasswordConfirm)
	sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.editing {
		t.Fatal("enter on the last field should save the form")
	}
}

func TestForwardFormBuildsLocalSpec(t *testing.T) {
	hosts := testHosts()
	var gotHost string
	var gotSpec coreconnector.ForwardSpec
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		Forward: func(host sshconfig.Host, spec coreconnector.ForwardSpec) tea.Cmd {
			gotHost = host.Alias
			gotSpec = spec
			return tea.Quit
		},
	})
	msg := m.Init()()
	m.Update(msg)

	sendKey(m, runeKey("p"))
	if !m.forwardMode || !strings.Contains(plainView(m), "FORWARD / prod-db") {
		t.Fatalf("forward form missing:\n%s", plainView(m))
	}
	cmd := sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("forward submission did not quit the TUI")
	}
	if gotHost != "prod-db" {
		t.Fatalf("host = %q", gotHost)
	}
	want := coreconnector.ForwardSpec{Kind: coreconnector.ForwardLocal, BindHost: "127.0.0.1", BindPort: 8080, DestinationHost: "localhost", DestinationPort: 22}
	if gotSpec != want {
		t.Fatalf("spec = %#v, want %#v", gotSpec, want)
	}
	if m.forwardMode {
		t.Fatal("forward form stayed open")
	}
}

func TestForwardFormSupportsDynamicSOCKS(t *testing.T) {
	hosts := testHosts()
	var gotSpec coreconnector.ForwardSpec
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		Forward: func(_ sshconfig.Host, spec coreconnector.ForwardSpec) tea.Cmd {
			gotSpec = spec
			return tea.Quit
		},
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("p"))
	sendKey(m, runeKey("3"))
	if got := m.forwardKind(); got != coreconnector.ForwardDynamic {
		t.Fatalf("kind = %q, want dynamic", got)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	sendKey(m, runeKey("127.0.0.1:1080"))
	sendKey(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	want := coreconnector.ForwardSpec{Kind: coreconnector.ForwardDynamic, BindHost: "127.0.0.1", BindPort: 1080}
	if gotSpec != want {
		t.Fatalf("spec = %#v, want %#v", gotSpec, want)
	}
}

func TestSFTPShortcutStartsBrowser(t *testing.T) {
	hosts := testHosts()
	var gotHost string
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		SFTP: func(host sshconfig.Host) tea.Cmd {
			gotHost = host.Alias
			return tea.Quit
		},
	})
	msg := m.Init()()
	m.Update(msg)
	cmd := sendKey(m, runeKey("s"))
	if cmd == nil || gotHost != "prod-db" {
		t.Fatalf("SFTP shortcut host=%q cmd=%v", gotHost, cmd)
	}
}

func TestForwardTypeUsesSelectorAndSkipsDynamicDestination(t *testing.T) {
	hosts := testHosts()
	m := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return hosts, nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
		Forward: func(sshconfig.Host, coreconnector.ForwardSpec) tea.Cmd { return nil },
	})
	msg := m.Init()()
	m.Update(msg)
	sendKey(m, runeKey("p"))

	if m.forwardFocus != 0 || m.forwardKind() != coreconnector.ForwardLocal {
		t.Fatalf("initial selector focus=%d kind=%q", m.forwardFocus, m.forwardKind())
	}
	if m.forwardFields[0].label != "Bind (local)" || m.forwardFields[1].label != "Destination (remote)" {
		t.Fatalf("local labels = %q / %q", m.forwardFields[0].label, m.forwardFields[1].label)
	}
	if view := plainView(m); !strings.Contains(view, "[1 local]") || !strings.Contains(view, "2 remote") || !strings.Contains(view, "3 dynamic") {
		t.Fatalf("type selector missing:\n%s", view)
	}

	sendKey(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.forwardKind() != coreconnector.ForwardRemote {
		t.Fatalf("right did not select remote: %q", m.forwardKind())
	}
	sendKey(m, runeKey("3"))
	if m.forwardKind() != coreconnector.ForwardDynamic {
		t.Fatalf("3 did not select dynamic: %q", m.forwardKind())
	}
	if m.forwardFields[0].label != "Bind (local)" {
		t.Fatalf("dynamic bind label = %q", m.forwardFields[0].label)
	}
	if m.forwardFieldCount() != 2 {
		t.Fatalf("dynamic field count = %d, want 2", m.forwardFieldCount())
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.forwardFocus != 1 {
		t.Fatalf("tab focus = %d, want bind field", m.forwardFocus)
	}
	sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.forwardFocus != 0 {
		t.Fatalf("tab should skip destination and wrap to type, got %d", m.forwardFocus)
	}
	if view := plainView(m); !strings.Contains(view, "Destination: none") {
		t.Fatalf("dynamic destination hint missing:\n%s", view)
	}
	sendKey(m, runeKey("1"))
	if m.forwardKind() != coreconnector.ForwardLocal {
		t.Fatalf("1 did not select local: %q", m.forwardKind())
	}
}
