package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#005F87", Dark: "#5FD7FF"})
	styleAccent   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#005F87", Dark: "#5FD7FF"})
	styleFocus    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#005F87", Dark: "#5FD7FF"})
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#6C6C6C", Dark: "#808080"})
	styleRule     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C9D2D6", Dark: "#3A4A50"})
	styleSelected = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#D7EEF7", Dark: "#1F3A46"}).Foreground(lipgloss.AdaptiveColor{Light: "#003B4A", Dark: "#E6FBFF"})
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

	if m.diffMode {
		bodyRows := screenBodyRows(height, false)
		title, meta := m.diffScreenTitle()
		return renderScreen(
			width,
			height,
			viewSectionHeader(title, meta, width),
			"",
			m.viewDiff(width, bodyRows),
			m.viewFooter(width),
			m.viewStatus(width),
		)
	}
	if m.editing {
		title, meta := m.editScreenTitle()
		return m.renderSecondary(width, height, title, meta, m.viewEdit(width))
	}
	if m.notesMode {
		return m.renderSecondary(width, height, "NOTES "+m.notesTarget.Alias, "UNSAVED", m.viewNotes(width))
	}
	if m.forwardMode {
		return m.renderSecondary(width, height, "FORWARD / "+m.forwardTarget.Alias, "FORM", m.viewForward(width))
	}
	if m.macroMode {
		return m.renderSecondary(width, height, "MACROS / "+m.macroTarget.Alias, "", m.viewMacros(width))
	}
	if m.accessMode {
		return m.renderSecondary(width, height, "ACCESS / "+m.accessTarget.Alias, "", m.viewAccess(width))
	}
	if m.help {
		return m.renderSecondary(width, height, "KEYBOARD", "", m.viewHelp(width, screenBodyRows(height, false)))
	}

	bodyRows := screenBodyRows(height, true)
	return renderScreen(
		width,
		height,
		m.viewHeader(width),
		m.viewFilter(width),
		m.viewMainBody(width, bodyRows),
		m.viewFooter(width),
		m.viewStatus(width),
	)
}

func (m *Model) renderSecondary(width, height int, title, meta string, body []string) string {
	return renderScreen(
		width,
		height,
		viewSectionHeader(title, meta, width),
		"",
		body,
		m.viewFooter(width),
		m.viewStatus(width),
	)
}

func (m *Model) editScreenTitle() (string, string) {
	title := "EDIT " + m.editTarget.Alias
	meta := "UNSAVED"
	if m.creating {
		title = "NEW HOST"
		if m.copying {
			title = "COPY / " + m.copySource
		}
	}
	return title, meta
}

func (m *Model) diffScreenTitle() (string, string) {
	added, removed := 0, 0
	for _, line := range m.diffLines {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			removed++
		}
	}
	return "CONFIG DIFF / " + m.diffTarget.Alias, fmt.Sprintf("+%d -%d", added, removed)
}

func (m *Model) viewHeader(width int) string {
	left := styleTitle.Render(" whop") + styleDim.Render(" / hosts")
	if path := displayConfigPath(m.configPath); path != "" && width >= 72 {
		left += styleDim.Render("   " + path)
	}
	return leftRightLine(left, styleAccent.Render(m.modeLabel()), width)
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
	count := fmt.Sprintf("%d/%d hosts", len(m.filtered), len(m.allHosts))
	return leftRightLine(label+value, styleDim.Render(count), width)
}

func (m *Model) viewMainBody(width, rows int) []string {
	listWidth, detailWidth, split := splitPaneWidths(width)
	if split {
		left := m.viewListForPane(listWidth, rows, true)
		right := m.viewDetail(detailWidth)
		return joinPaneLines(left, right, listWidth, detailWidth, rows)
	}

	summary := m.selectedSummary()
	listRows := rows
	if summary != "" && rows > 1 {
		listRows = rows - 1
	}
	list := m.viewListForPane(width, listRows, false)
	list = fitLines(list, listRows, width)
	if summary != "" && rows > 1 {
		list = append(list, styleDim.Render(ansi.Truncate("  "+summary, width, "~")))
	}
	return list
}

func splitPaneWidths(width int) (listWidth, detailWidth int, ok bool) {
	if width < 100 {
		return width, 0, false
	}
	detailWidth = width * 32 / 100
	if detailWidth < 32 {
		detailWidth = 32
	}
	if detailWidth > 44 {
		detailWidth = 44
	}
	listWidth = width - detailWidth - 3
	if listWidth < 58 {
		return width, 0, false
	}
	return listWidth, detailWidth, true
}

func joinPaneLines(left, right []string, listWidth, detailWidth, rows int) []string {
	result := make([]string, 0, rows)
	for index := 0; index < rows; index++ {
		line := ""
		if index < len(left) {
			line = left[index]
		}
		detail := ""
		if index < len(right) {
			detail = right[index]
		}
		result = append(result, padTo(line, listWidth)+" "+styleRule.Render("│")+" "+padTo(detail, detailWidth))
	}
	return result
}

func (m *Model) viewList(width, height int) []string {
	return m.viewListForPane(width, height, false)
}

func (m *Model) viewListForPane(width, height int, split bool) []string {
	compactFlags := width < 60
	flagsWidth := m.flagsColumnWidth(compactFlags)
	var cols columns
	if split {
		cols = splitListColumns(width, m.showAll)
	} else {
		cols = listColumnsFor(width, m.showAll)
	}
	cols = withFlagsColumn(cols, flagsWidth)

	flagsHeader := ""
	if cols.showFlags {
		flagsHeader = "FLAGS"
		if compactFlags {
			flagsHeader = "FLG"
		}
	}
	accessHeader := ""
	if cols.showAccess {
		accessHeader = "ACCESS"
	}

	showHeader := height >= 3
	dataRows := height
	lines := make([]string, 0, height)
	if showHeader {
		lines = append(lines, styleDim.Render(padTo(rowLine(cols, "  ", flagsHeader, "NAME", "HOST", "USER", "PORT", accessHeader, "TAGS"), width)))
		dataRows--
	}
	if dataRows < 0 {
		dataRows = 0
	}
	if len(m.filtered) == 0 {
		lines = append(lines, m.emptyListLines(width)...)
		return lines
	}

	start := 0
	if m.cursor >= dataRows {
		start = m.cursor - dataRows + 1
	}
	end := start + dataRows
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
		flags := ""
		if cols.showFlags {
			flags = renderHostBadges(m.hostBadges(host), compactFlags, flagsWidth)
		}
		tags := strings.Join(host.Tags, ",")
		if tags == "" {
			tags = "-"
		}
		access := string(host.EffectiveAccess())
		line := padTo(rowLine(cols, prefix, flags, host.Alias, host.HostName, host.User, fmt.Sprintf("%d", host.Port), access, tags), width)
		if selected {
			line = styleSelected.Render(padTo(line, width))
		}
		lines = append(lines, line)
	}
	return lines
}

// hostBadge is a compact row-level status marker. Adding a new host status
// only requires adding it to hostBadges; list and detail rendering stay shared.
type hostBadge struct {
	Label       string
	Compact     string
	DetailLabel string
	DetailValue string
}

func (m *Model) hostBadges(host sshconfig.Host) []hostBadge {
	badges := make([]hostBadge, 0, 2)
	if host.Favorite {
		badges = append(badges, hostBadge{
			Label:       "*",
			Compact:     "*",
			DetailLabel: "favorite",
			DetailValue: "yes",
		})
	}
	if m.record || host.Record {
		badges = append(badges, hostBadge{
			Label:       "REC",
			Compact:     "R",
			DetailLabel: "recording",
			DetailValue: "on",
		})
	}
	return badges
}

func (m *Model) hasHostBadges() bool {
	for _, host := range m.allHosts {
		if len(m.hostBadges(host)) > 0 {
			return true
		}
	}
	return false
}

func (m *Model) flagsColumnWidth(compact bool) int {
	if !m.hasHostBadges() {
		return 0
	}
	header := "FLAGS"
	limit := 8
	if compact {
		header = "FLG"
		limit = 4
	}
	width := ansi.StringWidth(header)
	for _, host := range m.allHosts {
		width = max(width, ansi.StringWidth(renderHostBadges(m.hostBadges(host), compact, limit)))
	}
	return width
}

func renderHostBadges(badges []hostBadge, compact bool, limit int) string {
	if len(badges) == 0 {
		return "-"
	}
	labels := make([]string, 0, len(badges))
	for _, badge := range badges {
		label := badge.Label
		if compact && badge.Compact != "" {
			label = badge.Compact
		}
		labels = append(labels, label)
	}
	if ansi.StringWidth(strings.Join(labels, " ")) <= limit {
		return strings.Join(labels, " ")
	}

	visible := make([]string, 0, len(labels))
	used := 0
	for _, label := range labels {
		separator := 0
		if len(visible) > 0 {
			separator = 1
		}
		labelWidth := ansi.StringWidth(label)
		if used+separator+labelWidth > limit {
			overflow := fmt.Sprintf("+%d", len(labels)-len(visible))
			if len(visible) > 0 && used+1+ansi.StringWidth(overflow) <= limit {
				return strings.Join(visible, " ") + " " + overflow
			}
			return ansi.Truncate(overflow, limit, "~")
		}
		visible = append(visible, label)
		used += separator + labelWidth
	}
	return strings.Join(visible, " ")
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
		message = "no login hosts found"
		hint = "a show all entries"
	}
	return []string{
		styleDim.Render(ansi.Truncate("  "+message, width, "~")),
		styleDim.Render(ansi.Truncate("  "+hint, width, "~")),
	}
}

func (m *Model) viewDetail(width int) []string {
	host, ok := m.selected()
	if !ok {
		return nil
	}
	if width < 1 {
		return nil
	}

	identity := strings.Join(host.IdentityFiles, ", ")
	proxy := host.ProxyJump
	tags := strings.Join(host.Tags, ", ")

	lines := []string{
		styleAccent.Render(fit(strings.ToUpper(host.Alias), width)),
		detailLine("host", host.Address(), width),
		detailLine("user", valueOrDash(host.User), width),
	}
	if identity != "" {
		lines = append(lines, detailLine("identity", identity, width))
	}
	if proxy != "" {
		lines = append(lines, detailLine("proxy", proxy, width))
	}
	for _, badge := range m.hostBadges(host) {
		lines = append(lines, detailLine(badge.DetailLabel, badge.DetailValue, width))
	}
	if tags != "" {
		lines = append(lines, detailLine("tags", tags, width))
	}
	if host.Notes != "" {
		lines = append(lines, detailLine("notes", host.Notes, width))
	}
	if m.showAll {
		lines = append(lines, detailLine("access", hostAccessDescription(host), width))
	}
	return lines
}

func (m *Model) viewDiff(width, rows int) []string {
	m.clampDiffOffset()
	lines := make([]string, 0, rows)
	end := min(len(m.diffLines), m.diffOffset+rows)
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
	return lines
}

func (m *Model) viewEdit(width int) []string {
	lines := make([]string, 0, len(m.editFields)+4)
	if m.copying {
		lines = append(lines, ansi.Truncate(styleDim.Render("  Prefilled fields select on focus; type to replace. Password and history are not copied."), width, "~"), "")
	}
	for index, field := range m.editFields {
		prefix := "  "
		if index == m.editFocus {
			prefix = "> "
		}
		lines = append(lines, ansi.Truncate(prefix+field.AlignedView(max(1, width-2)), width, "~"))
		if index == 6 {
			lines = append(lines, "")
		}
	}
	passwordHint := "  Password optional; if set, saved in the encrypted vault"
	if !m.creating {
		passwordHint = "  Leave password blank to keep the current password"
	}
	lines = append(lines,
		"",
		styleDim.Render("  IdentityFile blank inherits Host * / OpenSSH defaults"),
		styleDim.Render(ansi.Truncate(passwordHint, width, "~")),
	)
	return lines
}

func (m *Model) viewForward(width int) []string {
	kind := m.forwardKind()
	lines := make([]string, 0, 12)

	typePrefix := "  "
	if m.forwardFocus == 0 {
		typePrefix = "> "
	}
	lines = append(lines, ansi.Truncate(typePrefix+m.viewForwardType(), width, "~"))
	lines = append(lines, styleDim.Render("    left/right or 1/2/3 to choose type"), "")

	bindPrefix := "  "
	if m.forwardFocus == 1 {
		bindPrefix = "> "
	}
	lines = append(lines, ansi.Truncate(bindPrefix+m.forwardFields[0].View(), width, "~"))
	if kind == coreconnector.ForwardDynamic {
		lines = append(lines, styleDim.Render("    Destination: none (chosen by each SOCKS5 request)"))
	} else {
		destinationPrefix := "  "
		if m.forwardFocus == 2 {
			destinationPrefix = "> "
		}
		lines = append(lines, ansi.Truncate(destinationPrefix+m.forwardFields[1].View(), width, "~"))
	}

	lines = append(lines, "")
	switch kind {
	case coreconnector.ForwardRemote:
		lines = append(lines,
			styleDim.Render("    Listener: remote SSH host"),
			styleDim.Render("    Destination: dialed from this machine"),
		)
	case coreconnector.ForwardDynamic:
		lines = append(lines,
			styleDim.Render("    Listener: this machine (SOCKS5 proxy)"),
			styleDim.Render("    Destination: supplied by each SOCKS5 client"),
		)
	default:
		lines = append(lines,
			styleDim.Render("    Listener: this machine"),
			styleDim.Render("    Destination: dialed from the remote SSH host"),
		)
	}
	return lines
}

func (m *Model) viewForwardType() string {
	parts := make([]string, 0, len(m.forwardType.options))
	for index, option := range m.forwardType.options {
		label := fmt.Sprintf("%d %s", index+1, option)
		if index == m.forwardType.selected {
			label = "[" + label + "]"
			if m.forwardType.focused {
				label = styleSelected.Render(label)
			} else {
				label = styleAccent.Render(label)
			}
		} else {
			label = styleDim.Render(" " + label + " ")
		}
		parts = append(parts, label)
	}
	return m.forwardType.label + ":   " + strings.Join(parts, " ")
}

func (m *Model) viewNotes(width int) []string {
	return []string{ansi.Truncate("  "+m.notesInput.View(), width, "~")}
}

func (m *Model) viewMacros(width int) []string {
	lines := make([]string, 0, len(m.macroNames)+1)
	if len(m.macroNames) == 0 {
		return []string{styleDim.Render("  no macros")}
	}
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
	return lines
}

func (m *Model) viewAccess(width int) []string {
	lines := []string{
		styleDim.Render(ansi.Truncate("  current: "+hostAccessDescription(m.accessTarget), width, "~")),
		"",
	}
	for index, mode := range m.accessOptions {
		prefix := "  "
		if index == m.accessCursor {
			prefix = "> "
		}
		line := ansi.Truncate(prefix+accessChoiceLabel(mode), width, "~")
		if index == m.accessCursor {
			line = styleSelected.Render(padTo(line, max(1, width-1)))
		}
		lines = append(lines, line)
	}
	return lines
}

func accessChoiceLabel(mode sshconfig.AccessMode) string {
	switch mode {
	case sshconfig.AccessLogin:
		return "login host"
	case sshconfig.AccessService:
		return "service endpoint"
	default:
		return "automatic"
	}
}

func (m *Model) viewHelp(width, rows int) []string {
	navigation := []string{
		"  enter         connect to selected host",
		"  /             search hosts",
		"  tab / esc     apply search",
		"  esc           clear search",
		"  up/down, j/k  move",
		"  g / G         first / last",
		"  ctrl+d/u      page down / up",
		"  q             quit",
	}
	hosts := []string{
		"  a             login hosts / all entries",
		"  L             set login access",
		"  f             toggle favorite (*)",
		"  N             new connection",
		"  c             copy selected host",
		"  e             edit connection",
		"  n             edit notes",
		"  p             port forwarding",
		"  s             SFTP file browser",
	}
	sessions := []string{
		"  m             run macro",
		"  R             recording on/off (REC)",
		"  r             reload config",
	}

	if width >= 96 {
		left := append([]string{styleAccent.Render(" NAVIGATION")}, navigation...)
		left = append(left, "", styleAccent.Render(" SESSIONS"))
		left = append(left, sessions...)
		right := append([]string{styleAccent.Render(" HOSTS")}, hosts...)
		lines := joinHelpColumns(left, right, width)
		if len(lines) > rows {
			lines = lines[:rows]
		}
		return lines
	}

	lines := []string{styleAccent.Render(" NAVIGATION")}
	lines = append(lines, navigation...)
	lines = append(lines, "", styleAccent.Render(" HOSTS"))
	lines = append(lines, hosts...)
	lines = append(lines, "", styleAccent.Render(" SESSIONS"))
	lines = append(lines, sessions...)
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return lines
}

func joinHelpColumns(left, right []string, width int) []string {
	gap := 3
	leftWidth := (width - gap) * 55 / 100
	rightWidth := width - leftWidth - gap
	rows := max(len(left), len(right))
	lines := make([]string, 0, rows)
	for index := 0; index < rows; index++ {
		l, r := "", ""
		if index < len(left) {
			l = ansi.Truncate(left[index], leftWidth, "~")
		}
		if index < len(right) {
			r = ansi.Truncate(right[index], rightWidth, "~")
		}
		lines = append(lines, padTo(l, leftWidth)+strings.Repeat(" ", gap)+r)
	}
	return lines
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
	} else if m.forwardMode {
		if m.forwardFocus == 0 {
			hints = []footerHint{
				{key: "left/right", action: "type"},
				{key: "1/2/3", action: "type"},
				{key: "tab/enter", action: "next"},
				{key: "ctrl+s", action: "start"},
				{key: "esc", action: "cancel"},
			}
		} else {
			hints = []footerHint{
				{key: "tab/enter", action: "next"},
				{key: "ctrl+s", action: "start"},
				{key: "esc", action: "cancel"},
			}
		}
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
	} else if m.accessMode {
		hints = []footerHint{
			{key: "j/k", action: "choose"},
			{key: "enter", action: "apply"},
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
		if m.showAll {
			hints = []footerHint{
				{key: "enter", action: "connect"},
				{key: "/", action: "search"},
				{key: "L", action: "access"},
				{key: "a", action: "login hosts"},
				{key: "?", action: "help"},
				{key: "q", action: "quit"},
			}
		} else {
			hints = []footerHint{
				{key: "enter", action: "connect"},
				{key: "/", action: "search"},
				{key: "j/k", action: "move"},
				{key: "a", action: "all entries"},
				{key: "?", action: "help"},
				{key: "q", action: "quit"},
			}
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
	if len(hints) == 0 {
		return ""
	}

	leftHints := make([]footerHint, 0, len(hints))
	rightHints := make([]footerHint, 0, 2)
	for _, hint := range hints {
		switch hint.key {
		case "?", "q", "?/esc":
			rightHints = append(rightHints, hint)
		default:
			leftHints = append(leftHints, hint)
		}
	}

	renderItem := func(hint footerHint) string {
		return styleKey.Render(hint.key) + " " + styleDim.Render(hint.action)
	}
	renderHints := func(items []footerHint) string {
		parts := make([]string, 0, len(items))
		for _, hint := range items {
			parts = append(parts, renderItem(hint))
		}
		return strings.Join(parts, "   ")
	}

	right := renderHints(rightHints)
	rightWidth := ansi.StringWidth(right)
	leftBudget := width
	if rightWidth > 0 {
		leftBudget = width - rightWidth - 2
	}
	if leftBudget < 1 {
		return fit(right, width)
	}

	selected := make([]footerHint, 0, len(leftHints))
	used := 0
	for _, hint := range leftHints {
		itemWidth := ansi.StringWidth(renderItem(hint))
		separator := 0
		if len(selected) > 0 {
			separator = 3
		}
		if used+separator+itemWidth > leftBudget {
			continue
		}
		selected = append(selected, hint)
		used += separator + itemWidth
	}

	left := renderHints(selected)
	line := " " + left
	if right != "" {
		gap := width - ansi.StringWidth(line) - rightWidth
		if gap < 1 {
			gap = 1
		}
		line += strings.Repeat(" ", gap) + right
	}
	return fit(line, width)
}

func (m *Model) viewStatus(width int) string {
	if !m.loaded && m.status.current.Text == "" {
		return styleDim.Render(" loading...")
	}
	return m.status.render(width, m.selectedAlias())
}

type columns struct {
	name, host, user, port, tags, access, flags int
	showUser, showPort, showTags, showAccess    bool
	showFlags                                   bool
}

func listColumns(width int) columns {
	return listColumnsFor(width, false)
}

func listColumnsFor(width int, showAll bool) columns {
	switch {
	case width >= 100:
		if showAll {
			return columns{
				name: 24, host: 30, user: 12, port: 5, access: 8,
				tags:     max(6, width-91),
				showUser: true, showPort: true, showAccess: true, showTags: true,
			}
		}
		return columns{
			name: 24, host: 30, user: 12, port: 5,
			tags:     max(8, width-81),
			showUser: true, showPort: true, showTags: true,
		}
	case width >= 72:
		hostWidth := max(14, width-53)
		if showAll {
			return columns{name: 20, host: hostWidth, user: 10, port: 5, access: 8, showUser: true, showPort: true, showAccess: true}
		}
		return columns{name: 20, host: hostWidth, user: 10, port: 5, tags: 8, showUser: true, showPort: true, showTags: true}
	case width >= 52:
		userWidth := min(10, max(8, width/6))
		hostWidth := max(12, width-24-userWidth)
		return columns{name: 18, host: hostWidth, user: userWidth, showUser: true}
	default:
		return columns{name: 16, host: max(8, width-20)}
	}
}

func splitListColumns(width int, showAll bool) columns {
	nameWidth := 18
	// Two leading cursor columns, two inter-column gaps, and the tags or
	// access cell are part of the pane width budget.
	hostWidth := max(12, width-32)
	if showAll {
		return columns{name: nameWidth, host: hostWidth, access: 8, showAccess: true}
	}
	return columns{name: nameWidth, host: hostWidth, tags: 8, showTags: true}
}

func withFlagsColumn(columns columns, flagsWidth int) columns {
	if flagsWidth <= 0 {
		return columns
	}
	columns.showFlags = true
	columns.flags = flagsWidth
	extra := flagsWidth + 2
	if extra > 0 {
		reduce := min(extra, max(0, columns.host-8))
		columns.host -= reduce
		extra -= reduce
	}
	if extra > 0 {
		reduce := min(extra, max(0, columns.name-10))
		columns.name -= reduce
		extra -= reduce
	}
	if extra > 0 && columns.showTags {
		reduce := min(extra, max(0, columns.tags-6))
		columns.tags -= reduce
	}
	return columns
}

func rowLine(columns columns, prefix, flags, name, host, user, port, access, tags string) string {
	cells := make([]string, 0, 7)
	if columns.showFlags {
		cells = append(cells, fit(flags, columns.flags))
	}
	cells = append(cells, fit(name, columns.name), fit(host, columns.host))
	if columns.showUser {
		cells = append(cells, fit(user, columns.user))
	}
	if columns.showPort {
		cells = append(cells, fitRight(port, columns.port))
	}
	if columns.showAccess {
		cells = append(cells, fit(access, columns.access))
	}
	if columns.showTags {
		cells = append(cells, fit(tags, columns.tags))
	}
	return prefix + strings.Join(cells, "  ")
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
