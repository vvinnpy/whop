package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// StatusResult notices confirm a completed action. They expire automatically.
// StatusProgress notices describe an operation that is still running.
// StatusError notices describe a failure and remain until the user acts.
type StatusLevel uint8

const (
	StatusResult StatusLevel = iota + 1
	StatusProgress
	StatusError
)

const statusResultTTL = 3 * time.Second

// Notice is a status-line message. Scope limits host-specific notices to the
// selected host; errors keep their scope in the text but stay visible.
type Notice struct {
	Text  string
	Level StatusLevel
	Scope string
}

type statusNotice struct {
	Notice
	id uint64
}

type statusState struct {
	current statusNotice
	nextID  uint64
	timerID uint64
}

type statusExpiredMsg struct {
	id uint64
}

func (s *statusState) set(notice Notice) {
	if notice.Text == "" {
		s.clear()
		return
	}
	if notice.Level == 0 {
		notice.Level = StatusResult
	}
	s.nextID++
	s.current = statusNotice{Notice: notice, id: s.nextID}
}

func (s *statusState) result(text, scope string) {
	s.set(Notice{Text: text, Level: StatusResult, Scope: scope})
}

func (s *statusState) progress(text, scope string) {
	s.set(Notice{Text: text, Level: StatusProgress, Scope: scope})
}

func (s *statusState) fail(text, scope string) {
	s.set(Notice{Text: text, Level: StatusError, Scope: scope})
}

func (s *statusState) clear() {
	s.current = statusNotice{}
}

func (s *statusState) clearResult() {
	if s.current.Level == StatusResult {
		s.clear()
	}
}

func (s *statusState) clearProgress() {
	if s.current.Level == StatusProgress {
		s.clear()
	}
}

func (s *statusState) expire(id uint64) {
	if s.current.id == id && s.current.Level == StatusResult {
		s.clear()
	}
}

func (s *statusState) is(level StatusLevel, text string) bool {
	return s.current.Level == level && s.current.Text == text
}

func (s *statusState) text() string {
	return s.current.Text
}

func (s *statusState) arm(cmd tea.Cmd) tea.Cmd {
	if s.current.Level != StatusResult || s.current.Text == "" || s.current.id == s.timerID {
		return cmd
	}
	id := s.current.id
	s.timerID = id
	timer := tea.Tick(statusResultTTL, func(time.Time) tea.Msg {
		return statusExpiredMsg{id: id}
	})
	if cmd == nil {
		return timer
	}
	return tea.Batch(cmd, timer)
}

func (s *statusState) visible(selectedAlias string) (statusNotice, bool) {
	if s.current.Text == "" {
		return statusNotice{}, false
	}
	if s.current.Scope != "" && s.current.Scope != selectedAlias && s.current.Level != StatusError {
		return statusNotice{}, false
	}
	return s.current, true
}

func (s *statusState) render(width int, selectedAlias string) string {
	if width < 1 {
		width = 1
	}
	notice, ok := s.visible(selectedAlias)
	if !ok {
		return " "
	}
	text := ansi.Truncate(" "+notice.Text, width, "~")
	switch notice.Level {
	case StatusError:
		return styleError.Render(ansi.Truncate(" ! "+notice.Text, width, "~"))
	case StatusProgress:
		return styleAccent.Render(text)
	default:
		return styleDim.Render(text)
	}
}
