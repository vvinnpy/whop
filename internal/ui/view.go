package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#005F87", Dark: "#5FD7FF"})
	styleAccent   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#005F87", Dark: "#5FD7FF"})
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#6C6C6C", Dark: "#808080"})
	styleSelected = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#D7EEF7", Dark: "#1F3A46"}).Foreground(lipgloss.AdaptiveColor{Light: "#003B4A", Dark: "#E6FBFF"}).Bold(true)
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#AF0000", Dark: "#FF5F5F"})
	styleAdded    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#006B2E", Dark: "#5FD787"})
	styleRemoved  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9B0000", Dark: "#FF6B6B"})
	styleHunk     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#005F87", Dark: "#5FD7FF"}).Bold(true)
	styleKey      = styleAccent.Bold(true)
)

// View implements tea.Model.
func (m *Model) View() string {
	width := m.width
	if width <= 0 {
		width = 100
	}
	height := m.height
	if height <= 0 {
		height = 30
	}

	header := m.viewHeader(width)
	filter := m.viewFilter(width)
	if m.diffMode {
		return joinView(header, filter, m.viewDiff(width, height), m.viewFooter(width), m.viewStatus(width))
	}
	if m.editing {
		return joinView(header, filter, m.viewEdit(width), m.viewFooter(width), m.viewStatus(width))
	}
	if m.notesMode {
		return joinView(header, filter, m.viewNotes(width), m.viewFooter(width), m.viewStatus(width))
	}
	if m.macroMode {
		return joinView(header, filter, m.viewMacros(width), m.viewFooter(width), m.viewStatus(width))
	}
	if m.help {
		return joinView(header, filter, m.viewHelp(width), m.viewFooter(width))
	}

	list := m.viewList(width, height)
	if width < 96 {
		if summary := m.selectedSummary(); summary != "" {
			list = append(list, styleDim.Render(ansi.Truncate("  "+summary, width, "~")))
		}
	}
	detail := m.viewDetail(width)
	body := strings.Join(list, "\n")
	if detail != "" {
		body = joinColumns(list, strings.Split(detail, "\n"), width)
	}

	return joinView(header, filter, body, m.viewFooter(width), m.viewStatus(width))
}

func joinView(sections ...string) string {
	visible := make([]string, 0, len(sections))
	for _, section := range sections {
		if section != "" {
			visible = append(visible, section)
		}
	}
	return strings.Join(visible, "\n")
}

func (m *Model) viewHeader(width int) string {
	count := fmt.Sprintf("%d/%d hosts", len(m.filtered), len(m.allHosts))
	left := styleTitle.Render(" whop ") + styleDim.Render(" "+count)
	if path := displayConfigPath(m.configPath); path != "" && width >= 72 {
		left += styleDim.Render("  " + path)
	}
	modeText := m.modeLabel()
	if m.record {
		modeText += " · recording"
	}
	mode := styleAccent.Render(modeText)
	if ansi.StringWidth(left)+ansi.StringWidth(mode)+1 > width {
		left = ansi.Truncate(left, max(1, width-ansi.StringWidth(mode)-1), "~")
	}
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(mode)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + mode
}

func (m *Model) viewFilter(width int) string {
	label := styleAccent.Render(" / search: ")
	value := m.filter
	if m.filterFocused {
		value = m.filter + "█"
	} else if m.filter != "" {
		value = m.filter
	} else {
		value = styleDim.Render("(press / to search)")
	}
	return ansi.Truncate(label+value, width, "~")
}

func (m *Model) viewList(width, height int) []string {
	listWidth := width
	detailVisible := width >= 96
	if detailVisible {
		listWidth = max(55, width*3/5)
		if listWidth > width-30 {
			listWidth = width - 30
		}
	}

	cols := listColumns(listWidth)
	if detailVisible {
		// User and port remain visible in the detail pane, so the list
		// prefers tags in split view.
		available := max(22, listWidth-26)
		hostWidth := max(14, available*3/5)
		cols = columns{name: 20, host: hostWidth, tags: max(8, available-hostWidth), showTags: true}
	}
	lines := []string{styleDim.Render(padTo(rowLine(cols, "NAME", "HOST", "USER", "PORT", "TAGS"), listWidth))}

	available := height - 7
	if available < 3 {
		available = 3
	}
	if len(m.filtered) == 0 {
		lines = append(lines, m.emptyListLines(listWidth)...)
		return lines
	}

	start := 0
	if m.cursor >= available {
		start = m.cursor - available + 1
	}
	end := start + available
	if end > len(m.filtered) {
		end = len(m.filtered)
	}

	for index := start; index < end; index++ {
		host := m.filtered[index]
		selected := index == m.cursor
		prefix := "  "
		if selected {
			prefix = "> "
		}
		name := host.Alias
		if host.Favorite {
			name = "*" + name
		}
		tags := strings.Join(host.Tags, ",")
		if tags == "" {
			tags = "-"
		}
		line := padTo(rowLine(cols, prefix+name, host.HostName, host.User, fmt.Sprintf("%d", host.Port), tags), listWidth)
		if selected {
			line = styleSelected.Render(padTo(line, listWidth))
		}
		lines = append(lines, line)
	}
	return lines
}

func (m *Model) emptyListLines(width int) []string {
	var message, hint string
	switch {
	case len(m.allHosts) == 0:
		message = "no hosts in " + valueOrDash(m.configPath)
		hint = "N create a connection"
	case m.filter != "":
		message = fmt.Sprintf("no results for %q", m.filter)
		if m.filterFocused {
			hint = "ctrl+u clear search"
		} else {
			hint = "esc clear search"
		}
	default:
		message = "no servers found"
		hint = "a show all hosts"
	}
	return []string{
		styleDim.Render(ansi.Truncate("  "+message, width, "~")),
		styleDim.Render(ansi.Truncate("  "+hint, width, "~")),
	}
}

func (m *Model) viewDetail(width int) string {
	host, ok := m.selected()
	if !ok {
		return ""
	}

	if width < 96 {
		return ""
	}

	detailWidth := width - max(55, width*3/5) - 2
	if detailWidth < 24 {
		detailWidth = 24
	}
	identity := strings.Join(host.IdentityFiles, ", ")
	proxy := host.ProxyJump
	tags := strings.Join(host.Tags, ",")

	lines := []string{
		styleAccent.Render(fit(strings.ToUpper(host.Alias), detailWidth)),
		detailLine("host", host.Address(), detailWidth),
		detailLine("user", valueOrDash(host.User), detailWidth),
	}
	if identity != "" {
		lines = append(lines, detailLine("identity", identity, detailWidth))
	}
	if proxy != "" {
		lines = append(lines, detailLine("proxy", proxy, detailWidth))
	}
	if tags != "" {
		lines = append(lines, detailLine("tags", tags, detailWidth))
	}
	if host.Notes != "" {
		lines = append(lines, detailLine("notes", host.Notes, detailWidth))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewDiff(width, height int) string {
	lines := []string{styleAccent.Render(" CONFIG DIFF / " + m.diffTarget.Alias), ""}
	available := max(1, height-7)
	m.clampDiffOffset()
	end := min(len(m.diffLines), m.diffOffset+available)
	for _, line := range m.diffLines[m.diffOffset:end] {
		switch {
		case strings.HasPrefix(line, "+"):
			line = styleAdded.Render(line)
		case strings.HasPrefix(line, "-"):
			line = styleRemoved.Render(line)
		case strings.HasPrefix(line, "@@"):
			line = styleHunk.Render(line)
		default:
			line = styleDim.Render(line)
		}
		lines = append(lines, ansi.Truncate(line, width, "~"))
	}
	if len(m.diffLines) == 0 {
		lines = append(lines, styleDim.Render("  no changes"))
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

func (m *Model) viewEdit(width int) string {
	title := " EDIT " + m.editTarget.Alias
	if m.creating {
		title = " NEW HOST"
	}
	lines := []string{styleAccent.Render(title), ""}
	for index, field := range m.editFields {
		prefix := "  "
		if index == m.editFocus {
			prefix = "> "
		}
		lines = append(lines, ansi.Truncate(prefix+field.View(), width, "~"))
	}
	passwordHint := " Password optional; if set, saved in the encrypted vault"
	if !m.creating {
		passwordHint = " Leave password blank to keep the current password"
	}
	lines = append(lines,
		"",
		styleDim.Render(" IdentityFile blank inherits Host * / OpenSSH defaults"),
		styleDim.Render(passwordHint),
	)
	return strings.Join(lines, "\n")
}

func (m *Model) viewNotes(width int) string {
	lines := []string{
		styleAccent.Render(" NOTES " + m.notesTarget.Alias),
		"",
		ansi.Truncate("  "+m.notesInput.View(), width, "~"),
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewMacros(width int) string {
	lines := []string{styleAccent.Render(" MACROS / " + m.macroTarget.Alias), ""}
	if len(m.macroNames) == 0 {
		lines = append(lines, styleDim.Render("  no macros"))
	} else {
		for index, name := range m.macroNames {
			prefix := "  "
			if index == m.macroCursor {
				prefix = "> "
			}
			line := ansi.Truncate(prefix+name, width, "~")
			if index == m.macroCursor {
				line = styleSelected.Render(padTo(line, max(1, width-1)))
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewHelp(width int) string {
	lines := []string{
		styleAccent.Render(" KEYBOARD"),
		"",
		styleAccent.Render(" NAVIGATION"),
		"  enter         connect to selected host",
		"  /             search hosts",
		"  tab / esc     apply search",
		"  esc           clear search",
		"  up/down, j/k  move",
		"  g / G         first / last",
		"  ctrl+d/u      page down / up",
		"  q             quit",
		styleAccent.Render(" HOSTS"),
		"  a             servers / all hosts",
		"  f             toggle favorite (*)",
		"  N             new connection",
		"  e             edit connection",
		"  n             edit notes",
		styleAccent.Render(" SESSIONS"),
		"  m             run macro",
		"  R             recording on/off",
		"  r             reload config",
		"",
		"  ?             close help",
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "~")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewFooter(width int) string {
	var hints []footerHint
	if m.editing {
		if m.editFocus < len(m.editFields)-1 {
			hints = []footerHint{
				{key: "tab/enter", action: "next"},
				{key: "ctrl+s", action: "save"},
				{key: "esc", action: "cancel"},
			}
		} else {
			hints = []footerHint{
				{key: "enter/ctrl+s", action: "save"},
				{key: "esc", action: "cancel"},
			}
		}
	} else if m.notesMode {
		hints = []footerHint{{key: "ctrl+s", action: "save"}, {key: "esc", action: "cancel"}}
	} else if m.diffMode {
		hints = []footerHint{
			{key: "enter/y", action: "apply"},
			{key: "n/esc", action: "back"},
			{key: "j/k", action: "scroll"},
			{key: "ctrl+d/u", action: "page"},
		}
	} else if m.macroMode {
		hints = []footerHint{
			{key: "enter", action: "run"},
			{key: "j/k", action: "move"},
			{key: "esc", action: "cancel"},
		}
	} else if m.help {
		hints = []footerHint{{key: "?/esc", action: "close help"}}
	} else if m.filterFocused {
		hints = []footerHint{
			{key: "type", action: "search"},
			{key: "enter", action: "connect"},
			{key: "tab/esc", action: "apply"},
			{key: "ctrl+u", action: "clear"},
		}
	} else if m.filter != "" {
		hints = []footerHint{
			{key: "enter", action: "connect"},
			{key: "/", action: "edit search"},
			{key: "j/k", action: "move"},
			{key: "esc", action: "clear"},
			{key: "?", action: "help"},
			{key: "q", action: "quit"},
		}
	} else {
		hints = []footerHint{
			{key: "enter", action: "connect"},
			{key: "/", action: "search"},
			{key: "j/k", action: "move"},
			{key: "?", action: "help"},
			{key: "q", action: "quit"},
		}
	}
	return renderFooterHints(width, hints)
}

type footerHint struct {
	key    string
	action string
}

func renderFooterHints(width int, hints []footerHint) string {
	if width < 1 {
		width = 1
	}
	const separator = "  "
	lines := make([]string, 0, 2)
	line := ""
	lineWidth := 0
	for _, hint := range hints {
		item := styleKey.Render(hint.key) + styleDim.Render(": "+hint.action)
		itemWidth := ansi.StringWidth(item)
		switch {
		case line == "":
			line = " " + item
			lineWidth = 1 + itemWidth
		case lineWidth+len(separator)+itemWidth <= width:
			line += separator + item
			lineWidth += len(separator) + itemWidth
		default:
			lines = append(lines, line)
			line = " " + item
			lineWidth = 1 + itemWidth
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "~")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewStatus(width int) string {
	if m.err != nil {
		return styleError.Render(ansi.Truncate(" error: "+m.err.Error(), width, "~"))
	}
	if m.status == "" {
		if m.loaded {
			return ""
		}
		return styleDim.Render(" loading...")
	}
	return styleDim.Render(ansi.Truncate(" "+m.status, width, "~"))
}

type columns struct {
	name, host, user, port, tags int
	showUser, showPort, showTags bool
}

func listColumns(width int) columns {
	switch {
	case width >= 100:
		return columns{name: 24, host: 30, user: 12, port: 5, tags: max(8, width-24-30-12-5-6), showUser: true, showPort: true, showTags: true}
	case width >= 72:
		return columns{name: 20, host: max(16, width-49), user: 10, port: 5, tags: 8, showUser: true, showPort: true, showTags: true}
	case width >= 52:
		hostWidth := max(12, (width-18-8)/2)
		userWidth := max(8, width-18-hostWidth-6)
		return columns{name: 18, host: hostWidth, user: userWidth, showUser: true}
	default:
		return columns{name: 16, host: max(8, width-18)}
	}
}

func rowLine(columns columns, name, host, user, port, tags string) string {
	line := "  " + fit(name, columns.name) + " " + fit(host, columns.host)
	if columns.showUser {
		line += " " + fit(user, columns.user)
	}
	if columns.showPort {
		line += " " + fit(port, columns.port)
	}
	if columns.showTags {
		line += " " + fit(tags, columns.tags)
	}
	return line
}

func detailLine(label, value string, width int) string {
	labelWidth := 9
	left := styleDim.Render(fit(label, labelWidth))
	return left + " " + fit(valueOrDash(value), max(1, width-labelWidth-1))
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func joinColumns(left, right []string, width int) string {
	leftWidth := max(55, width*3/5)
	if leftWidth > width-30 {
		leftWidth = width - 30
	}
	rows := max(len(left), len(right))
	lines := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, padTo(l, leftWidth)+"  "+r)
	}
	return strings.Join(lines, "\n")
}

func fit(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = ansi.Truncate(value, width, "~")
	gap := width - ansi.StringWidth(value)
	if gap > 0 {
		value += strings.Repeat(" ", gap)
	}
	return value
}

func padTo(value string, width int) string {
	return fit(value, width)
}

func displayConfigPath(path string) string {
	if path == "" || path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == home {
		if path == home {
			return "~"
		}
		return path
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
