package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(value string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}

func TestTextFieldEditing(t *testing.T) {
	field := newTextField("Host", "prod", 40)
	field.Focus()
	field.Update(key("-db"))
	if field.Value() != "prod-db" {
		t.Fatalf("value = %q", field.Value())
	}
	field.Update(tea.KeyMsg{Type: tea.KeyLeft})
	field.Update(tea.KeyMsg{Type: tea.KeyLeft})
	field.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if field.Value() != "proddb" {
		t.Fatalf("unexpected value after backspace: %q", field.Value())
	}
	field.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if field.Value() != "" {
		t.Fatalf("ctrl+u did not clear: %q", field.Value())
	}
	field.Update(key("x"))
	if field.Value() != "x" {
		t.Fatalf("value = %q", field.Value())
	}
}

func TestTextFieldViewAndTrim(t *testing.T) {
	field := newTextField("Host", "  prod  ", 20)
	field.Focus()
	view := field.View()
	if !strings.Contains(view, "Host:") || !strings.Contains(view, "prod") {
		t.Fatalf("view = %q", view)
	}
	if field.String() != "prod" {
		t.Fatalf("trimmed = %q", field.String())
	}
}
