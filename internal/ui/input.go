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
	line := f.label + ": " + value
	if f.width > 0 {
		return ansi.Truncate(line, f.width, "~")
	}
	return line
}

func (f *textField) String() string { return strings.TrimSpace(string(f.value)) }
