package automation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMacroExecution(t *testing.T) {
	stream := newFakeStream()
	stream.Feed("Password: shell>")
	macro := Macro{
		Name: "login",
		Steps: []Step{
			{Kind: StepSend, Send: []byte("login\n")},
			{Name: "password-prompt", Kind: StepWait, Pattern: `Password:`, Timeout: time.Second, MaxRetries: 1},
			{Name: "password", Kind: StepSend, Send: []byte("s3cret\n"), Secret: true},
			{Name: "shell-prompt", Kind: StepWait, Pattern: `shell>`, Timeout: time.Second},
			{Kind: StepSleep, Duration: time.Millisecond},
			{Kind: StepSend, Send: []byte("uptime\n")},
		},
	}
	var events []Event
	err := RunMacro(context.Background(), stream, macro, Options{
		CloseStream:    true,
		OverallTimeout: time.Second,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("run macro: %v", err)
	}
	if got := stream.Output(); got != "login\ns3cret\nuptime\n" {
		t.Fatalf("output = %q", got)
	}
	if !hasEvent(events, EventMacroCompleted) {
		t.Fatalf("missing macro completion event: %#v", events)
	}
	for _, event := range events {
		if strings.Contains(event.Message, "s3cret") {
			t.Fatalf("audit event leaked secret: %#v", event)
		}
	}
}

func TestMacroValidation(t *testing.T) {
	tests := []struct {
		name  string
		macro Macro
	}{
		{name: "missing name", macro: Macro{Steps: []Step{{Kind: StepSend, Send: []byte("x")}}}},
		{name: "no steps", macro: Macro{Name: "empty"}},
		{name: "send without data", macro: Macro{Name: "bad", Steps: []Step{{Kind: StepSend}}}},
		{name: "wait without pattern", macro: Macro{Name: "bad", Steps: []Step{{Kind: StepWait}}}},
		{name: "invalid regex", macro: Macro{Name: "bad", Steps: []Step{{Kind: StepWait, Pattern: "["}}}},
		{name: "sleep without duration", macro: Macro{Name: "bad", Steps: []Step{{Kind: StepSleep}}}},
		{name: "unknown kind", macro: Macro{Name: "bad", Steps: []Step{{Kind: "shell"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.macro.Validate(); !errors.Is(err, ErrInvalidMacro) {
				t.Fatalf("expected invalid macro, got %v", err)
			}
		})
	}
}

func TestMacroSecretStepCannotRepeat(t *testing.T) {
	macro := Macro{
		Name: "repeat-secret",
		Steps: []Step{
			{Name: "password", Kind: StepSend, Send: []byte("one\n"), Secret: true},
			{Name: "password", Kind: StepSend, Send: []byte("two\n"), Secret: true},
		},
	}
	if err := macro.Validate(); !errors.Is(err, ErrInvalidMacro) {
		t.Fatalf("expected duplicate secret validation error, got %v", err)
	}
}
