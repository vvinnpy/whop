package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// textField is a small single-line editor used by the TUI forms.
// It intentionally keeps the project free of clipboard-related dependencies.
type textField struct {
	label    string
	value    []rune
	cursor   int
	focused  bool
	width    int
	secret   bool
	selected bool
}

func newTextField(label, value string, width int) textField {
	runes := []rune(value)
	return textField{label: label, value: runes, cursor: len(runes), width: width}
}

func newSecretField(label string, width int) textField {
	return textField{label: label, width: width, secret: true}
}

func (f *textField) SetValue(value string) {
	f.value = []rune(value)
	f.cursor = len(f.value)
	f.selected = false
}

func (f *textField) Value() string { return string(f.value) }

func (f *textField) Focus() { f.focused = true }
func (f *textField) Blur() {
	f.focused = false
	f.selected = false
}

// SelectAll marks the current value for replacement by the next edit.
func (f *textField) SelectAll() {
	if len(f.value) > 0 {
		f.selected = true
		f.cursor = len(f.value)
	}
}

func (f *textField) Update(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "left":
		f.selected = false
		if f.cursor > 0 {
			f.cursor--
		}
	case "right":
		f.selected = false
		if f.cursor < len(f.value) {
			f.cursor++
		}
	case "home", "ctrl+a":
		f.selected = false
		f.cursor = 0
	case "end", "ctrl+e":
		f.selected = false
		f.cursor = len(f.value)
	case "backspace":
		if f.selected {
			f.value = nil
			f.cursor = 0
			f.selected = false
		} else if f.cursor > 0 {
			f.value = append(f.value[:f.cursor-1], f.value[f.cursor:]...)
			f.cursor--
		}
	case "delete":
		if f.selected {
			f.value = nil
			f.cursor = 0
			f.selected = false
		} else if f.cursor < len(f.value) {
			f.value = append(f.value[:f.cursor], f.value[f.cursor+1:]...)
		}
	case "ctrl+u":
		f.value = nil
		f.cursor = 0
		f.selected = false
	default:
		if len(msg.Runes) > 0 {
			if f.selected {
				f.value = nil
				f.cursor = 0
				f.selected = false
			}
			insert := []rune(msg.Runes)
			next := make([]rune, 0, len(f.value)+len(insert))
			next = append(next, f.value[:f.cursor]...)
			next = append(next, insert...)
			next = append(next, f.value[f.cursor:]...)
			f.value = next
			f.cursor += len(insert)
		}
	}
	return nil
}

func (f *textField) View() string {
	return f.viewWithLabelWidth(0, f.width)
}

// AlignedView renders the field with a fixed label gutter so adjacent form
// fields line up. width includes the label and value.
func (f *textField) AlignedView(width int) string {
	return f.viewWithLabelWidth(18, width)
}

func (f *textField) viewWithLabelWidth(labelWidth, width int) string {
	value := string(f.value)
	if f.secret {
		if len(f.value) > 0 {
			value = "********"
		}
	} else if f.focused && f.selected {
		value = "\x1b[7m" + string(f.value) + "\x1b[0m"
	} else if f.focused {
		before := string(f.value[:f.cursor])
		after := string(f.value[f.cursor:])
		value = before + "█" + after
	}

	label := f.label + ":"
	if labelWidth > 0 {
		label = fit(label, labelWidth)
	}
	line := label + " " + value
	if width > 0 {
		return ansi.Truncate(line, width, "~")
	}
	return line
}

func (f *textField) String() string { return strings.TrimSpace(string(f.value)) }

// choiceField is a fixed-option selector used instead of free-form text input.
// It is intentionally keyboard-only and keeps the selected option explicit in
// rendered output, so it remains understandable without color.
type choiceField struct {
	label    string
	options  []string
	selected int
	focused  bool
}

func newChoiceField(label string, options []string, selected int) choiceField {
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	return choiceField{
		label:    label,
		options:  append([]string(nil), options...),
		selected: selected,
	}
}

func (f *choiceField) Focus() { f.focused = true }
func (f *choiceField) Blur()  { f.focused = false }

func (f *choiceField) Value() string {
	if len(f.options) == 0 {
		return ""
	}
	return f.options[f.selected]
}

func (f *choiceField) Move(delta int) {
	if len(f.options) == 0 {
		return
	}
	next := f.selected + delta
	if next < 0 {
		next = 0
	}
	if next >= len(f.options) {
		next = len(f.options) - 1
	}
	f.selected = next
}

func (f *choiceField) Select(index int) {
	if index >= 0 && index < len(f.options) {
		f.selected = index
	}
}

// Update handles selection keys and returns true when the selection changed.
func (f *choiceField) Update(msg tea.KeyMsg) bool {
	before := f.selected
	switch msg.String() {
	case "left", "up", "k":
		f.Move(-1)
	case "right", "down", "j":
		f.Move(1)
	default:
		if len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '9' {
			f.Select(int(msg.Runes[0] - '1'))
		}
	}
	return f.selected != before
}
