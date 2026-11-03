// Package ui implements the ttydeck terminal user interface.
package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"ttydeck/internal/core"
	"ttydeck/internal/sshconfig"
)

// HostLoader loads all SSH config entries.
type HostLoader func() ([]sshconfig.Host, error)

// ConnectFunc starts a connection and returns a Bubble Tea command.
type ConnectFunc func(sshconfig.Host, bool) tea.Cmd

// Options configures the TUI model.
type Options struct {
	ConfigPath     string
	InitialFilter  string
	ShowAll        bool
	Record         bool
	Load           HostLoader
	Connect        ConnectFunc
	ToggleFavorite func(sshconfig.Host, bool) error
}

// ConnectFinishedMsg is emitted after an interactive session returns.
type ConnectFinishedMsg struct {
	Err error
}

type hostsLoadedMsg struct {
	hosts []sshconfig.Host
	err   error
}

// Model is the ttydeck list/detail screen.
type Model struct {
	configPath     string
	load           HostLoader
	connect        ConnectFunc
	toggleFavorite func(sshconfig.Host, bool) error

	allHosts []sshconfig.Host
	filtered []sshconfig.Host
	cursor   int

	filter        string
	filterFocused bool
	showAll       bool
	record        bool
	help          bool

	width  int
	height int

	loaded bool
	status string
	err    error
}

// New creates a new TUI model.
func New(opts Options) *Model {
	m := &Model{
		configPath:     opts.ConfigPath,
		load:           opts.Load,
		connect:        opts.Connect,
		toggleFavorite: opts.ToggleFavorite,
		filter:         opts.InitialFilter,
		showAll:        opts.ShowAll,
		record:         opts.Record,
		width:          100,
		height:         30,
	}
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return m.reload
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampCursor()
		return m, nil

	case hostsLoadedMsg:
		m.loaded = true
		if msg.err != nil {
			m.err = msg.err
			m.status = "failed to load hosts"
			return m, nil
		}
		m.err = nil
		m.allHosts = msg.hosts
		m.applyFilter()
		m.status = fmt.Sprintf("%d servers / %d total", len(sshconfig.Servers(m.allHosts)), len(m.allHosts))
		return m, nil

	case ConnectFinishedMsg:
		if msg.Err != nil {
			m.err = msg.Err
			m.status = "connection failed"
		} else {
			m.err = nil
			m.status = "session ended"
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	if m.help {
		switch msg.String() {
		case "?", "esc", "q":
			m.help = false
		}
		return m, nil
	}

	if m.filterFocused {
		switch msg.String() {
		case "esc":
			m.filter = ""
			m.filterFocused = false
			m.applyFilter()
			return m, nil
		case "enter":
			m.filterFocused = false
			return m.connectSelected()
		case "backspace":
			m.removeLastFilterRune()
			return m, nil
		case "ctrl+u":
			m.filter = ""
			m.applyFilter()
			return m, nil
		case "up":
			m.move(-1)
			return m, nil
		case "down":
			m.move(1)
			return m, nil
		case "tab":
			m.filterFocused = false
			return m, nil
		}
		if len(msg.Runes) > 0 {
			m.filter += string(msg.Runes)
			m.applyFilter()
		}
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "/":
		m.filterFocused = true
		return m, nil
	case "enter":
		return m.connectSelected()
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g":
		m.cursor = 0
	case "G":
		if len(m.filtered) > 0 {
			m.cursor = len(m.filtered) - 1
		}
	case "ctrl+d":
		m.move(m.pageSize())
	case "ctrl+u":
		m.move(-m.pageSize())
	case "r":
		m.status = "reloading..."
		return m, m.reload
	case "a":
		m.showAll = !m.showAll
		m.applyFilter()
		if m.showAll {
			m.status = "showing all hosts"
		} else {
			m.status = "showing servers only"
		}
	case "f":
		m.toggleSelectedFavorite()
	case "R":
		m.record = !m.record
		if m.record {
			m.status = "recording enabled for next session"
		} else {
			m.status = "recording disabled"
		}
	case "?":
		m.help = true
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.applyFilter()
		} else {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Model) reload() tea.Msg {
	if m.load == nil {
		return hostsLoadedMsg{}
	}
	hosts, err := m.load()
	return hostsLoadedMsg{hosts: hosts, err: err}
}

func (m *Model) connectSelected() (tea.Model, tea.Cmd) {
	host, ok := m.selected()
	if !ok {
		return m, nil
	}
	if m.connect == nil {
		m.err = fmt.Errorf("connect is not configured")
		m.status = "connection failed"
		return m, nil
	}
	m.err = nil
	m.status = fmt.Sprintf("connecting to %s...", host.Alias)
	return m, m.connect(host, m.record)
}

func (m *Model) toggleSelectedFavorite() {
	host, ok := m.selected()
	if !ok || m.toggleFavorite == nil {
		return
	}
	next := !host.Favorite
	if err := m.toggleFavorite(host, next); err != nil {
		m.err = err
		m.status = "favorite update failed"
		return
	}
	m.err = nil
	for i := range m.allHosts {
		if m.allHosts[i].Alias == host.Alias {
			m.allHosts[i].Favorite = next
		}
	}
	m.applyFilter()
	m.status = fmt.Sprintf("%s favorite=%v", host.Alias, next)
}

func (m *Model) selected() (sshconfig.Host, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return sshconfig.Host{}, false
	}
	return m.filtered[m.cursor], true
}

func (m *Model) applyFilter() {
	previous := ""
	if host, ok := m.selected(); ok {
		previous = host.Alias
	}

	source := m.allHosts
	if !m.showAll {
		source = sshconfig.Servers(source)
	}
	source = core.SortHosts(source)
	m.filtered = core.Filter(source, m.filter)
	m.cursor = 0
	if previous != "" {
		for i, host := range m.filtered {
			if host.Alias == previous {
				m.cursor = i
				break
			}
		}
	}
	m.clampCursor()
}

func (m *Model) move(delta int) {
	m.cursor += delta
	m.clampCursor()
}

func (m *Model) pageSize() int {
	size := m.height - 10
	if size < 1 {
		size = 1
	}
	if size > 10 {
		size = 10
	}
	return size
}

func (m *Model) clampCursor() {
	if len(m.filtered) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
}

func (m *Model) removeLastFilterRune() {
	runes := []rune(m.filter)
	if len(runes) > 0 {
		m.filter = string(runes[:len(runes)-1])
	}
	m.applyFilter()
}

func (m *Model) filterSummary() string {
	if m.filter == "" {
		return "(type to filter)"
	}
	return m.filter
}

func (m *Model) modeLabel() string {
	if m.showAll {
		return "all"
	}
	return "servers"
}

func (m *Model) selectedSummary() string {
	host, ok := m.selected()
	if !ok {
		return ""
	}
	parts := []string{host.Alias, host.Address()}
	if host.User != "" {
		parts = append(parts, "user "+host.User)
	}
	if len(host.Tags) > 0 {
		parts = append(parts, "tags "+strings.Join(host.Tags, ","))
	}
	return strings.Join(parts, "  ")
}
