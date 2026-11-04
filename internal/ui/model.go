// Package ui implements the whop terminal user interface.
package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"whop/internal/core"
	"whop/internal/sshconfig"
)

const (
	editFieldAlias = iota
	editFieldHostName
	editFieldUser
	editFieldPort
	editFieldProxyJump
	editFieldIdentityFile
	editFieldTags
	editFieldPassword
	editFieldPasswordConfirm
)

// HostLoader loads all SSH config entries.
type HostLoader func() ([]sshconfig.Host, error)

// ConnectFunc starts a connection and returns a Bubble Tea command.
type ConnectFunc func(sshconfig.Host, bool) tea.Cmd

// MacroLoader returns the names of stored automation macros.
type MacroLoader func() ([]string, error)

// MacroRunner starts a named macro against one host.
type MacroRunner func(sshconfig.Host, string) tea.Cmd

// Options configures the TUI model.
type Options struct {
	ConfigPath      string
	InitialFilter   string
	InitialStatus   string
	ShowAll         bool
	Record          bool
	Load            HostLoader
	Connect         ConnectFunc
	LoadMacros      MacroLoader
	RunMacro        MacroRunner
	ToggleFavorite  func(sshconfig.Host, bool) error
	PreviewEdit     func(sshconfig.Host, sshconfig.Edit) (sshconfig.Preview, error)
	EditHost        func(sshconfig.Host, sshconfig.Edit) error
	PreviewAdd      func(sshconfig.HostImport) (sshconfig.Preview, error)
	AddHost         func(sshconfig.HostImport, string) error
	RenameHost      func(oldAlias, newAlias string) error
	SetHostPassword func(sshconfig.Host, string) error
	SetTags         func(sshconfig.Host, []string) error
	SaveNotes       func(sshconfig.Host, string) error
	SetLoginMode    func(sshconfig.Host, sshconfig.AccessMode) error
}

// ConnectFinishedMsg is emitted after an interactive session returns.
type ConnectFinishedMsg struct {
	Err error
}

// MacroFinishedMsg is emitted after a named macro run returns.
type MacroFinishedMsg struct {
	Host string
	Name string
	Err  error
}

type hostsLoadedMsg struct {
	hosts []sshconfig.Host
	err   error
}

// Model is the whop list/detail screen.
type Model struct {
	configPath      string
	load            HostLoader
	connect         ConnectFunc
	loadMacros      MacroLoader
	runMacro        MacroRunner
	toggleFavorite  func(sshconfig.Host, bool) error
	previewEdit     func(sshconfig.Host, sshconfig.Edit) (sshconfig.Preview, error)
	editHost        func(sshconfig.Host, sshconfig.Edit) error
	previewAdd      func(sshconfig.HostImport) (sshconfig.Preview, error)
	addHost         func(sshconfig.HostImport, string) error
	renameHost      func(oldAlias, newAlias string) error
	setHostPassword func(sshconfig.Host, string) error
	setTags         func(sshconfig.Host, []string) error
	saveNotes       func(sshconfig.Host, string) error
	setLoginMode    func(sshconfig.Host, sshconfig.AccessMode) error

	allHosts []sshconfig.Host
	filtered []sshconfig.Host
	cursor   int

	filter        string
	filterFocused bool
	initialStatus string
	showAll       bool
	record        bool
	help          bool

	width  int
	height int

	loaded bool
	status string
	err    error

	editing              bool
	creating             bool
	editFields           []textField
	editFocus            int
	editTarget           sshconfig.Host
	editAliasInitial     string
	editHostNameInitial  string
	editUserInitial      string
	editPortInitial      int
	editProxyJumpInitial string
	editIdentityInitial  string

	diffMode        bool
	diffLines       []string
	diffOffset      int
	diffTarget      sshconfig.Host
	pendingEdit     sshconfig.Edit
	pendingAdd      *sshconfig.HostImport
	pendingAddHash  string
	pendingTags     []string
	pendingPassword string

	notesMode   bool
	notesInput  textField
	notesTarget sshconfig.Host

	macroMode   bool
	macroNames  []string
	macroCursor int
	macroTarget sshconfig.Host

	accessMode    bool
	accessTarget  sshconfig.Host
	accessOptions []sshconfig.AccessMode
	accessCursor  int
}

// New creates a new TUI model.
func New(opts Options) *Model {
	m := &Model{
		configPath:      opts.ConfigPath,
		load:            opts.Load,
		connect:         opts.Connect,
		loadMacros:      opts.LoadMacros,
		runMacro:        opts.RunMacro,
		toggleFavorite:  opts.ToggleFavorite,
		previewEdit:     opts.PreviewEdit,
		editHost:        opts.EditHost,
		previewAdd:      opts.PreviewAdd,
		addHost:         opts.AddHost,
		renameHost:      opts.RenameHost,
		setHostPassword: opts.SetHostPassword,
		setTags:         opts.SetTags,
		saveNotes:       opts.SaveNotes,
		setLoginMode:    opts.SetLoginMode,
		filter:          opts.InitialFilter,
		initialStatus:   opts.InitialStatus,
		showAll:         opts.ShowAll,
		record:          opts.Record,
		width:           100,
		height:          30,
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
		if m.initialStatus != "" {
			m.status = m.initialStatus
			m.initialStatus = ""
		}
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

	case MacroFinishedMsg:
		if msg.Err != nil {
			m.err = msg.Err
			m.status = fmt.Sprintf("macro %s failed on %s", msg.Name, msg.Host)
		} else {
			m.err = nil
			m.status = fmt.Sprintf("macro %s completed on %s", msg.Name, msg.Host)
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

	if m.editing {
		return m.updateEdit(msg)
	}
	if m.diffMode {
		return m.updateDiff(msg)
	}
	if m.notesMode {
		return m.updateNotes(msg)
	}
	if m.macroMode {
		return m.updateMacros(msg)
	}
	if m.accessMode {
		return m.updateAccess(msg)
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
		case "esc", "tab":
			m.applyFilterAndExit()
			return m, nil
		case "enter":
			m.filterFocused = false
			return m.connectSelected()
		case "backspace":
			m.removeLastFilterRune()
			m.status = ""
			return m, nil
		case "ctrl+u":
			m.filter = ""
			m.applyFilter()
			m.status = ""
			return m, nil
		case "up", "ctrl+p":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n":
			m.move(1)
			return m, nil
		}
		if len(msg.Runes) > 0 {
			m.filter += string(msg.Runes)
			m.applyFilter()
			m.status = ""
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
	case "j", "down", "ctrl+n":
		m.move(1)
	case "k", "up", "ctrl+p":
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
			m.status = "showing all entries"
		} else {
			m.status = "showing login hosts"
		}
	case "f":
		m.toggleSelectedFavorite()
	case "N":
		m.startCreate()
	case "e":
		m.startEdit()
	case "n":
		m.startNotes()
	case "m":
		m.startMacros()
	case "L":
		m.startAccess()
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
			m.status = ""
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

func (m *Model) startMacros() {
	host, ok := m.selected()
	if !ok {
		return
	}
	if m.loadMacros == nil {
		m.err = fmt.Errorf("macros are not configured")
		m.status = "macro list unavailable"
		return
	}
	names, err := m.loadMacros()
	if err != nil {
		m.err = err
		m.status = "failed to load macros"
		return
	}
	if len(names) == 0 {
		m.err = nil
		m.status = "no macros; create one with: whop macro set"
		return
	}
	m.macroNames = names
	m.macroCursor = 0
	m.macroTarget = host
	m.macroMode = true
	m.err = nil
	m.status = ""
}

func (m *Model) updateMacros(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "m":
		m.macroMode = false
		m.status = "macro cancelled"
		return m, nil
	case "j", "down", "ctrl+n":
		if len(m.macroNames) > 0 {
			m.macroCursor = (m.macroCursor + 1) % len(m.macroNames)
		}
		return m, nil
	case "k", "up", "ctrl+p":
		if len(m.macroNames) > 0 {
			m.macroCursor = (m.macroCursor - 1 + len(m.macroNames)) % len(m.macroNames)
		}
		return m, nil
	case "enter":
		if len(m.macroNames) == 0 {
			return m, nil
		}
		if m.runMacro == nil {
			m.err = fmt.Errorf("macro execution is not configured")
			m.status = "macro failed"
			return m, nil
		}
		name := m.macroNames[m.macroCursor]
		host := m.macroTarget
		m.macroMode = false
		m.err = nil
		m.status = fmt.Sprintf("running macro %s on %s...", name, host.Alias)
		return m, m.runMacro(host, name)
	}
	return m, nil
}

func (m *Model) startAccess() {
	host, ok := m.selected()
	if !ok {
		return
	}
	if m.setLoginMode == nil {
		m.err = fmt.Errorf("access changes are not configured")
		m.status = "access unavailable"
		return
	}
	m.accessOptions = []sshconfig.AccessMode{
		sshconfig.AccessAuto,
		sshconfig.AccessLogin,
		sshconfig.AccessService,
	}
	m.accessCursor = 0
	switch host.AccessMode {
	case sshconfig.AccessLogin:
		m.accessCursor = 1
	case sshconfig.AccessService:
		m.accessCursor = 2
	}
	m.accessTarget = host
	m.accessMode = true
	m.err = nil
	m.status = ""
}

func (m *Model) updateAccess(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.accessMode = false
		m.status = "access cancelled"
		return m, nil
	case "j", "down", "ctrl+n":
		m.accessCursor = (m.accessCursor + 1) % len(m.accessOptions)
		return m, nil
	case "k", "up", "ctrl+p":
		m.accessCursor = (m.accessCursor - 1 + len(m.accessOptions)) % len(m.accessOptions)
		return m, nil
	case "enter":
		mode := m.accessOptions[m.accessCursor]
		if err := m.setLoginMode(m.accessTarget, mode); err != nil {
			m.err = err
			m.status = "access update failed"
			return m, nil
		}
		host := m.accessTarget
		host.AccessMode = mode
		m.accessMode = false
		m.err = nil
		if mode == sshconfig.AccessAuto {
			m.status = fmt.Sprintf("%s: %s (automatic)", host.Alias, host.EffectiveAccess())
		} else {
			m.status = fmt.Sprintf("%s: %s (manual)", host.Alias, host.EffectiveAccess())
		}
		if !m.showAll && host.EffectiveAccess() == sshconfig.AccessService {
			m.status += " · hidden; press a to show all"
		}
		return m, m.reload
	}
	return m, nil
}

func (m *Model) startEdit() {
	host, ok := m.selected()
	if !ok {
		return
	}
	width := m.width - 20
	if width < 20 {
		width = 20
	}
	identity := ""
	if len(host.IdentityFiles) == 1 {
		identity = host.IdentityFiles[0]
	}
	fields := []textField{
		newTextField("Name", host.Alias, width),
		newTextField("HostName", host.HostName, width),
		newTextField("User", host.User, width),
		newTextField("Port", strconv.Itoa(host.Port), width),
		newTextField("ProxyJump", host.ProxyJump, width),
		newTextField("IdentityFile", identity, width),
		newTextField("Tags", strings.Join(host.Tags, ","), width),
		newSecretField("Password", width),
		newSecretField("Confirm password", width),
	}
	m.editFields = fields
	m.editFocus = editFieldAlias
	m.editFields[m.editFocus].Focus()
	m.editTarget = host
	m.editAliasInitial = host.Alias
	m.editHostNameInitial = host.HostName
	m.editUserInitial = host.User
	m.editPortInitial = host.Port
	m.editProxyJumpInitial = host.ProxyJump
	m.editIdentityInitial = identity
	m.creating = false
	m.pendingAdd = nil
	m.pendingAddHash = ""
	m.editing = true
	m.err = nil
	m.status = ""
}

func (m *Model) startCreate() {
	width := m.width - 20
	if width < 20 {
		width = 20
	}
	fields := []textField{
		newTextField("Name", "", width),
		newTextField("HostName", "", width),
		newTextField("User", "", width),
		newTextField("Port", "22", width),
		newTextField("ProxyJump", "", width),
		newTextField("IdentityFile", "", width),
		newTextField("Tags", "", width),
		newSecretField("Password", width),
		newSecretField("Confirm password", width),
	}
	m.editFields = fields
	m.editFocus = editFieldAlias
	m.editFields[m.editFocus].Focus()
	m.editTarget = sshconfig.Host{}
	m.editAliasInitial = ""
	m.editHostNameInitial = ""
	m.editUserInitial = ""
	m.editPortInitial = 0
	m.editProxyJumpInitial = ""
	m.editIdentityInitial = ""
	m.creating = true
	m.pendingAdd = nil
	m.pendingAddHash = ""
	m.editing = true
	m.err = nil
	m.status = ""
}

func (m *Model) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.clearEditPassword()
		m.editing = false
		m.creating = false
		m.pendingAdd = nil
		m.pendingAddHash = ""
		m.status = "edit cancelled"
		return m, nil
	case "tab":
		m.focusEditField(1)
		return m, nil
	case "shift+tab":
		m.focusEditField(-1)
		return m, nil
	case "enter":
		if m.editFocus < len(m.editFields)-1 {
			m.focusEditField(1)
			return m, nil
		}
		return m.saveEdit()
	case "ctrl+s":
		return m.saveEdit()
	}
	cmd := m.editFields[m.editFocus].Update(msg)
	return m, cmd
}

func (m *Model) focusEditField(delta int) {
	next := (m.editFocus + delta + len(m.editFields)) % len(m.editFields)
	m.focusEditFieldIndex(next)
}

func (m *Model) focusEditFieldIndex(index int) {
	if index < 0 || index >= len(m.editFields) {
		return
	}
	m.editFields[m.editFocus].Blur()
	m.editFocus = index
	m.editFields[m.editFocus].Focus()
}

func (m *Model) saveEdit() (tea.Model, tea.Cmd) {
	port, err := strconv.Atoi(strings.TrimSpace(m.editFields[editFieldPort].Value()))
	if err != nil || port < 1 || port > 65535 {
		m.focusEditFieldIndex(editFieldPort)
		m.err = fmt.Errorf("invalid port: enter a number from 1 to 65535")
		m.status = "invalid port"
		return m, nil
	}
	alias := strings.TrimSpace(m.editFields[editFieldAlias].Value())
	if alias == "" {
		m.focusEditFieldIndex(editFieldAlias)
		m.err = fmt.Errorf("name is required")
		m.status = "invalid host name"
		return m, nil
	}
	hostName := strings.TrimSpace(m.editFields[editFieldHostName].Value())
	user := strings.TrimSpace(m.editFields[editFieldUser].Value())
	proxyJump := strings.TrimSpace(m.editFields[editFieldProxyJump].Value())
	identityFile := strings.TrimSpace(m.editFields[editFieldIdentityFile].Value())
	tags := splitTags(m.editFields[editFieldTags].Value())
	password := m.editFields[editFieldPassword].Value()
	if password != m.editFields[editFieldPasswordConfirm].Value() {
		m.focusEditFieldIndex(editFieldPasswordConfirm)
		m.err = fmt.Errorf("passwords do not match")
		m.status = "password confirmation does not match"
		return m, nil
	}

	if m.creating {
		if hostName == "" {
			m.focusEditFieldIndex(editFieldHostName)
			m.err = fmt.Errorf("HostName is required")
			m.status = "invalid hostname"
			return m, nil
		}
		imp := sshconfig.HostImport{
			Alias:        alias,
			HostName:     hostName,
			User:         user,
			Port:         port,
			ProxyJump:    proxyJump,
			IdentityFile: identityFile,
		}
		if m.previewAdd != nil {
			preview, err := m.previewAdd(imp)
			if err != nil {
				m.err = err
				m.status = "diff preview failed"
				return m, nil
			}
			if strings.TrimSpace(preview.Diff) != "" {
				m.pendingAdd = &imp
				m.pendingAddHash = preview.Hash
				m.pendingTags = tags
				m.pendingPassword = password
				m.diffTarget = sshconfig.Host{Alias: alias}
				m.diffLines = strings.Split(strings.TrimRight(preview.Diff, "\n"), "\n")
				m.diffOffset = 0
				m.editing = false
				m.diffMode = true
				m.err = nil
				m.status = "review new host"
				return m, nil
			}
		}
		return m.commitCreate(imp, tags, password, "")
	}

	edit := sshconfig.Edit{}
	if alias != m.editAliasInitial {
		edit.Alias = &alias
	}
	if hostName != m.editHostNameInitial {
		edit.HostName = &hostName
	}
	if user != m.editUserInitial {
		edit.User = &user
	}
	if port != m.editPortInitial {
		edit.Port = &port
	}
	if proxyJump != m.editProxyJumpInitial {
		edit.ProxyJump = &proxyJump
	}
	if identityFile != m.editIdentityInitial {
		edit.IdentityFile = &identityFile
	}
	if m.previewEdit != nil {
		preview, err := m.previewEdit(m.editTarget, edit)
		if err != nil {
			m.err = err
			m.status = "diff preview failed"
			return m, nil
		}
		if strings.TrimSpace(preview.Diff) != "" {
			edit.ExpectedHash = preview.Hash
			m.pendingEdit = edit
			m.pendingAdd = nil
			m.pendingTags = tags
			m.pendingPassword = password
			m.diffTarget = m.editTarget
			m.diffLines = strings.Split(strings.TrimRight(preview.Diff, "\n"), "\n")
			m.diffOffset = 0
			m.editing = false
			m.diffMode = true
			m.err = nil
			m.status = "review config changes"
			return m, nil
		}
	}
	return m.commitEdit(m.editTarget, edit, tags, password)
}

func (m *Model) commitEdit(host sshconfig.Host, edit sshconfig.Edit, tags []string, password string) (tea.Model, tea.Cmd) {
	if password != "" {
		if m.setHostPassword == nil {
			m.err = fmt.Errorf("password storage is not configured")
			m.status = "password not saved"
			return m, nil
		}
		if err := m.setHostPassword(host, password); err != nil {
			m.err = err
			m.status = "password not saved"
			return m, nil
		}
		m.clearEditPassword()
	}
	if m.editHost == nil {
		m.err = fmt.Errorf("edit is not configured")
		m.status = "edit failed"
		return m, nil
	}
	if err := m.editHost(host, edit); err != nil {
		m.err = err
		m.status = "edit failed"
		return m, nil
	}

	effectiveAlias := host.Alias
	if edit.Alias != nil {
		effectiveAlias = strings.TrimSpace(*edit.Alias)
	}
	if effectiveAlias != host.Alias && m.renameHost != nil {
		if err := m.renameHost(host.Alias, effectiveAlias); err != nil {
			return m.finishEdit(fmt.Errorf("host renamed but metadata migration failed: %w", err))
		}
	}
	if m.setTags != nil {
		effectiveHost := host
		effectiveHost.Alias = effectiveAlias
		if err := m.setTags(effectiveHost, tags); err != nil {
			return m.finishEdit(fmt.Errorf("host updated but tags were not saved: %w", err))
		}
	}
	return m.finishEdit(nil)
}

func (m *Model) commitCreate(imp sshconfig.HostImport, tags []string, password, expectedHash string) (tea.Model, tea.Cmd) {
	if m.addHost == nil {
		m.err = fmt.Errorf("create is not configured")
		m.status = "create failed"
		return m, nil
	}
	if err := m.addHost(imp, expectedHash); err != nil {
		m.err = err
		m.status = "create failed"
		return m, nil
	}
	host := sshconfig.Host{Alias: imp.Alias, HostName: imp.HostName, User: imp.User, Port: imp.Port, ProxyJump: imp.ProxyJump}
	if m.setTags != nil {
		if err := m.setTags(host, tags); err != nil {
			return m.finishCreate(fmt.Errorf("host created but tags were not saved: %w", err))
		}
	}
	if password != "" {
		if m.setHostPassword == nil {
			return m.finishCreate(fmt.Errorf("host created but password storage is not configured"))
		}
		if err := m.setHostPassword(host, password); err != nil {
			return m.finishCreate(fmt.Errorf("host created but password was not saved: %w", err))
		}
	}
	return m.finishCreate(nil)
}

func (m *Model) finishEdit(err error) (tea.Model, tea.Cmd) {
	m.clearEditPassword()
	m.pendingAdd = nil
	m.pendingAddHash = ""
	m.editing = false
	m.creating = false
	m.diffMode = false
	m.err = err
	if err != nil {
		m.status = "host updated with errors"
	} else {
		m.status = "host updated"
	}
	return m, m.reload
}

func (m *Model) finishCreate(err error) (tea.Model, tea.Cmd) {
	m.clearEditPassword()
	m.pendingAdd = nil
	m.pendingAddHash = ""
	m.editing = false
	m.creating = false
	m.diffMode = false
	m.err = err
	if err != nil {
		m.status = "host created with errors"
	} else {
		m.status = "host created"
	}
	return m, m.reload
}

func (m *Model) clearEditPassword() {
	if len(m.editFields) > editFieldPasswordConfirm {
		m.editFields[editFieldPassword].SetValue("")
		m.editFields[editFieldPasswordConfirm].SetValue("")
	}
	m.pendingPassword = ""
}

func (m *Model) updateDiff(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		if m.pendingAdd != nil {
			return m.commitCreate(*m.pendingAdd, m.pendingTags, m.pendingPassword, m.pendingAddHash)
		}
		return m.commitEdit(m.diffTarget, m.pendingEdit, m.pendingTags, m.pendingPassword)
	case "n", "esc", "q":
		m.diffMode = false
		m.editing = true
		m.err = nil
		if m.creating {
			m.status = "new host still open"
		} else {
			m.status = "edit still open"
		}
		return m, nil
	case "j", "down", "ctrl+n":
		m.diffOffset++
	case "k", "up", "ctrl+p":
		m.diffOffset--
	case "ctrl+d", "pgdown":
		m.diffOffset += m.diffPageSize()
	case "ctrl+u", "pgup":
		m.diffOffset -= m.diffPageSize()
	case "g":
		m.diffOffset = 0
	case "G":
		m.diffOffset = max(0, len(m.diffLines)-m.diffPageSize())
	}
	m.clampDiffOffset()
	return m, nil
}

func (m *Model) diffPageSize() int {
	size := m.height - 10
	if size < 1 {
		return 1
	}
	return min(size, 20)
}

func (m *Model) clampDiffOffset() {
	maxOffset := max(0, len(m.diffLines)-m.diffPageSize())
	if m.diffOffset < 0 {
		m.diffOffset = 0
	}
	if m.diffOffset > maxOffset {
		m.diffOffset = maxOffset
	}
}

func splitTags(value string) []string {
	var tags []string
	seen := make(map[string]bool)
	for _, tag := range strings.Split(value, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	return tags
}

func (m *Model) startNotes() {
	host, ok := m.selected()
	if !ok {
		return
	}
	width := m.width - 20
	if width < 20 {
		width = 20
	}
	field := newTextField("Notes", host.Notes, width)
	field.Focus()
	m.notesInput = field
	m.notesTarget = host
	m.notesMode = true
	m.err = nil
	m.status = ""
}

func (m *Model) updateNotes(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.notesMode = false
		m.status = "notes cancelled"
		return m, nil
	case "ctrl+s":
		if m.saveNotes == nil {
			m.err = fmt.Errorf("notes are not configured")
			m.status = "notes failed"
			return m, nil
		}
		if err := m.saveNotes(m.notesTarget, m.notesInput.Value()); err != nil {
			m.err = err
			m.status = "notes failed"
			return m, nil
		}
		m.notesMode = false
		m.err = nil
		m.status = "notes saved"
		return m, m.reload
	}
	cmd := m.notesInput.Update(msg)
	return m, cmd
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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

func (m *Model) applyFilterAndExit() {
	m.filterFocused = false
	m.status = ""
}

func (m *Model) removeLastFilterRune() {
	runes := []rune(m.filter)
	if len(runes) > 0 {
		m.filter = string(runes[:len(runes)-1])
	}
	m.applyFilter()
}

func (m *Model) modeLabel() string {
	if m.showAll {
		return "all entries"
	}
	return "login hosts"
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
	if m.showAll {
		parts = append(parts, "access "+hostAccessDescription(host))
	}
	if len(host.Tags) > 0 {
		parts = append(parts, "tags "+strings.Join(host.Tags, ","))
	}
	return strings.Join(parts, "  ")
}

func hostAccessDescription(host sshconfig.Host) string {
	source := "auto"
	if host.AccessMode == sshconfig.AccessLogin || host.AccessMode == sshconfig.AccessService {
		source = "manual"
	}
	return fmt.Sprintf("%s (%s)", host.EffectiveAccess(), source)
}
