package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// screenChromeRows returns the number of fixed rows used by the shared screen
// layout. A command row is optional for screens that expose a search/filter
// input directly below the header.
func screenChromeRows(hasCommand bool) int {
	rows := 5 // header, top rule, bottom rule, status, footer
	if hasCommand {
		rows++
	}
	return rows
}

func screenBodyRows(height int, hasCommand bool) int {
	return max(1, height-screenChromeRows(hasCommand))
}

// renderScreen keeps every screen on the same vertical grid:
//
//	header
//	[command]
//	rule
//	body
//	rule
//	status
//	footer
func renderScreen(width, height int, header, command string, body []string, footer, status string) string {
	bodyRows := screenBodyRows(height, command != "")
	body = fitLines(body, bodyRows, width)

	lines := make([]string, 0, height)
	lines = append(lines, fit(header, width))
	if command != "" {
		lines = append(lines, fit(command, width))
	}
	lines = append(lines, screenRule(width))
	lines = append(lines, body...)
	lines = append(lines, screenRule(width))
	lines = append(lines, fit(status, width))
	lines = append(lines, fit(footer, width))
	return strings.Join(lines, "\n")
}

func fitLines(lines []string, rows, width int) []string {
	result := make([]string, 0, rows)
	for index := 0; index < rows; index++ {
		line := ""
		if index < len(lines) {
			line = lines[index]
		}
		result = append(result, fit(line, width))
	}
	return result
}

func screenRule(width int) string {
	if width < 1 {
		width = 1
	}
	return styleRule.Render(strings.Repeat("─", width))
}

func leftRightLine(left, right string, width int) string {
	if width < 1 {
		return ""
	}
	rightWidth := ansi.StringWidth(right)
	if rightWidth >= width {
		return ansi.Truncate(right, width, "~")
	}
	leftWidth := width - rightWidth - 1
	if leftWidth < 1 {
		leftWidth = 1
	}
	left = ansi.Truncate(left, leftWidth, "~")
	gap := width - ansi.StringWidth(left) - rightWidth
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func viewSectionHeader(title, meta string, width int) string {
	left := styleTitle.Render(" whop") + styleDim.Render(" / ") + styleAccent.Render(strings.TrimSpace(title))
	if meta == "" {
		return fit(left, width)
	}
	return leftRightLine(left, styleDim.Render(meta), width)
}

func fitRight(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = ansi.Truncate(value, width, "~")
	gap := width - ansi.StringWidth(value)
	if gap > 0 {
		return strings.Repeat(" ", gap) + value
	}
	return value
}
