package ui

import (
	"bytes"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"whop/internal/sshconfig"
)

func TestTeatestInitialRenderAndFilter(t *testing.T) {
	model := New(Options{
		ConfigPath: "~/.ssh/config",
		Load: func() ([]sshconfig.Host, error) {
			return testHosts(), nil
		},
		Connect: func(sshconfig.Host, bool) tea.Cmd { return nil },
	})

	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 30))
	defer tm.Quit()

	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return bytes.Contains(output, []byte("prod-db")) && bytes.Contains(output, []byte("whop"))
	}, teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	tm.Type("prod")
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return bytes.Contains(output, []byte("filter: prod"))
	}, teatest.WithDuration(3*time.Second))

	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}
