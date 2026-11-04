package ui

import (
	"fmt"
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
		return strings.Join([]string{header, filter, m.viewDiff(width, height), m.viewFooter(width), m.viewStatus(width)}, "\n")
	}
	if m.editing {
		return strings.Join([]string{header, filter, m.viewEdit(width), m.viewFooter(width), m.viewStatus(width)}, "\n")
	}
	if m.notesMode {
		return strings.Join([]string{header, filter, m.viewNotes(width), m.viewFooter(width), m.viewStatus(width)}, "\n")
	}
	if m.macroMode {
		return strings.Join([]string{header, filter, m.viewMacros(width), m.viewFooter(width), m.viewStatus(width)}, "\n")
	}
	if m.help {
		return strings.Join([]string{header, filter, m.viewHelp(width), m.viewFooter(width)}, "\n")
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

	return strings.Join([]string{header, filter, body, m.viewFooter(width), m.viewStatus(width)}, "\n")
}

func (m *Model) viewHeader(width int) string {
	count := fmt.Sprintf("%d/%d hosts", len(m.filtered), len(m.allHosts))
	left := styleTitle.Render(" whop ") + styleDim.Render(" "+count+"  "+m.configPath)
	modeText := m.modeLabel()
	if m.record {
		modeText += " rec"
	}
	mode := styleAccent.Render("[" + modeText + "]")
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
	label := styleAccent.Render(" / filter: ")
	value := m.filterSummary()
	if m.filterFocused {
		label = styleAccent.Render(" [FILTER EDIT] / filter: ")
		value = m.filter + "█"
	} else if m.filter != "" {
		label = styleAccent.Render(" [FILTER ACTIVE] / filter: ")
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
	if identity == "" {
		identity = "-"
	}
	proxy := host.ProxyJump
	if proxy == "" {
		proxy = "-"
	}
	tags := strings.Join(host.Tags, ",")
	if tags == "" {
		tags = "-"
	}

	last := "-"
	if !host.LastConnectedAt.IsZero() {
		last = host.LastConnectedAt.Local().Format("2006-01-02 15:04")
	}
	favorite := "no"
	if host.Favorite {
		favorite = "yes"
	}
	notes := host.Notes
	if notes == "" {
		notes = "-"
	}
	lines := []string{
		styleAccent.Render(fit(strings.ToUpper(host.Alias), detailWidth)),
		detailLine("host", host.Address(), detailWidth),
		detailLine("user", valueOrDash(host.User), detailWidth),
		detailLine("identity", identity, detailWidth),
		detailLine("proxy", proxy, detailWidth),
		detailLine("tags", tags, detailWidth),
		detailLine("kind", string(host.Kind), detailWidth),
		detailLine("favorite", favorite, detailWidth),
		detailLine("last", last, detailWidth),
		detailLine("count", fmt.Sprintf("%d", host.ConnectCount), detailWidth),
		detailLine("notes", notes, detailWidth),
		detailLine("source", host.Source, detailWidth),
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
	lines = append(lines, "", styleDim.Render(" IdentityFile blank inherits Host * / OpenSSH defaults"), styleDim.Render(" password blank keeps current; saved in encrypted vault"))
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
		"  enter       connect to selected host",
		"  /           focus filter",
		"  tab / esc   apply filter and return to list",
		"  j / k       move down / up",
		"  ctrl+p/n    move up / down",
		"  g / G       first / last",
		"  ctrl+d/u    page down / up",
		"  r           reload config",
		"  a           toggle servers / all",
		"  f           toggle favorite",
		"  N           create a new host",
		"  e           edit host name, identity, and password",
		"  n           edit notes",
		"  m           run an automation macro",
		"  R           toggle session recording",
		"  esc         list mode: clear filter or quit",
		"  ?           close help",
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "~")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewFooter(width int) string {
	var hint string
	if m.editing {
		hint = " tab next  enter save  esc cancel"
		if width >= 100 {
			hint += "  identity blank=default  password blank=keep"
		}
	} else if m.notesMode {
		hint = " ctrl+s save  esc cancel"
	} else if m.diffMode {
		hint = " enter/y apply  n/esc back  j/k scroll  ctrl+d/u page"
		if width < 72 {
			hint = " enter apply  n/esc back  j/k scroll"
		}
	} else if m.macroMode {
		hint = " enter run  j/k ctrl+p/n move  esc cancel"
		if width < 72 {
			hint = " enter run  esc cancel"
		}
	} else if m.help {
		hint = " ? / esc close help"
	} else if m.filterFocused {
		hint = " type filter  enter connect  tab/esc apply  ctrl+u clear"
		if width < 72 {
			hint = " type filter  enter connect  tab/esc apply"
		}
	} else if m.filter != "" {
		hint = " enter connect  / edit filter  j/k ctrl+p/n  e edit  esc clear"
		if width < 72 {
			hint = " enter connect  / edit search  esc clear"
		}
	} else {
		hint = " enter connect  / filter  j/k ctrl+p/n  N new  f fav  e edit  m macro  ? help  q quit"
		switch {
		case width < 52:
			hint = " enter connect  / search\n ? help  q quit"
		case width < 96:
			hint = " enter connect  / search  j/k move  e edit  ? help  q quit"
		}
	}
	rendered := make([]string, 0, 2)
	for _, line := range strings.Split(hint, "\n") {
		rendered = append(rendered, styleDim.Render(ansi.Truncate(line, width, "~")))
	}
	return strings.Join(rendered, "\n")
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
