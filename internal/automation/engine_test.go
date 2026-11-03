package automation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestEngineMatchesRuleAndSendsResponse(t *testing.T) {
	stream := newFakeStream()
	var events []Event
	engine, err := NewEngine(stream, Options{
		CloseStream: true,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	stream.Feed("login: ")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = engine.Expect(ctx, Rule{
		Name:    "login",
		Pattern: `login:\s*$`,
		Send:    []byte("alice\n"),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("expect: %v", err)
	}
	if got := stream.Output(); got != "alice\n" {
		t.Fatalf("output = %q", got)
	}
	if !hasEvent(events, EventRuleMatched) || !hasEvent(events, EventRuleSent) {
		t.Fatalf("missing audit events: %#v", events)
	}
}

func TestEngineRunsOrderedRulesAndPreservesReadAhead(t *testing.T) {
	stream := newFakeStream()
	engine, err := NewEngine(stream, Options{CloseStream: true})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	stream.Feed("user: password: shell>")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = engine.Run(ctx, []Rule{
		{Name: "user", Pattern: `user:`, Send: []byte("alice\n"), Timeout: time.Second},
		{Name: "password", Pattern: `password:`, Send: []byte("secret\n"), Timeout: time.Second, Secret: true},
		{Name: "shell", Pattern: `shell>`, Send: []byte("uptime\n"), Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stream.Output(); got != "alice\nsecret\nuptime\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestEngineRuleTimeout(t *testing.T) {
	stream := newFakeStream()
	var timeoutEvents int
	engine, err := NewEngine(stream, Options{
		CloseStream: true,
		OnEvent: func(event Event) {
			if event.Type == EventRuleTimeout {
				timeoutEvents++
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	err = engine.Expect(context.Background(), Rule{
		Name:    "never",
		Pattern: "never",
		Timeout: 20 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected timeout, got %v", err)
	}
	if timeoutEvents != 1 {
		t.Fatalf("timeout events = %d", timeoutEvents)
	}
}

func TestEngineOverallTimeout(t *testing.T) {
	stream := newFakeStream()
	engine, err := NewEngine(stream, Options{
		CloseStream:    true,
		OverallTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	err = engine.Run(context.Background(), []Rule{{Name: "never", Pattern: "never"}})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected overall timeout, got %v", err)
	}
}

func TestEngineContextCancellation(t *testing.T) {
	stream := newFakeStream()
	waiting := make(chan struct{}, 1)
	engine, err := NewEngine(stream, Options{
		CloseStream: true,
		OnEvent: func(event Event) {
			if event.Type == EventRuleWaiting {
				select {
				case waiting <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Expect(ctx, Rule{Name: "wait", Pattern: "ready", Timeout: time.Second})
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("engine did not start waiting")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expect did not stop after cancellation")
	}
}

func TestEngineRetriesAfterTimeout(t *testing.T) {
	stream := newFakeStream()
	retried := make(chan struct{}, 1)
	engine, err := NewEngine(stream, Options{
		CloseStream: true,
		OnEvent: func(event Event) {
			if event.Type == EventRuleRetry {
				select {
				case retried <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Expect(context.Background(), Rule{
			Name:       "slow-prompt",
			Pattern:    `Password:`,
			Send:       []byte("secret\n"),
			Timeout:    30 * time.Millisecond,
			MaxRetries: 1,
			Secret:     true,
		})
	}()
	select {
	case <-retried:
	case <-time.After(time.Second):
		t.Fatal("rule did not retry")
	}
	stream.Feed("Password:")
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expect: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expect did not complete after retry match")
	}
	if got := stream.Output(); got != "secret\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestEngineSecretRuleIsOneShot(t *testing.T) {
	stream := newFakeStream()
	var blocked bool
	engine, err := NewEngine(stream, Options{
		CloseStream: true,
		OnEvent: func(event Event) {
			if event.Type == EventSecretBlocked {
				blocked = true
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	rule := Rule{
		Name:    "password",
		Pattern: `Password:`,
		Send:    []byte("s3cret\n"),
		Timeout: time.Second,
		Secret:  true,
	}
	stream.Feed("Password:")
	if err := engine.Expect(context.Background(), rule); err != nil {
		t.Fatalf("first expect: %v", err)
	}
	err = engine.Expect(context.Background(), rule)
	if !errors.Is(err, ErrSecretAlreadySent) {
		t.Fatalf("expected one-shot error, got %v", err)
	}
	if !blocked {
		t.Fatal("missing secret-blocked audit event")
	}
	if got := stream.Output(); got != "s3cret\n" {
		t.Fatalf("secret sent more than once: %q", got)
	}
}

func TestEngineRejectsInvalidRules(t *testing.T) {
	stream := newFakeStream()
	engine, err := NewEngine(stream, Options{CloseStream: true})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	tests := []Rule{
		{Name: "empty-pattern"},
		{Name: "bad-pattern", Pattern: "["},
		{Name: "empty-match", Pattern: ".*"},
		{Name: "negative-timeout", Pattern: "x", Timeout: -time.Second},
		{Name: "negative-retry", Pattern: "x", MaxRetries: -1},
	}
	for _, rule := range tests {
		if err := engine.Expect(context.Background(), rule); !errors.Is(err, ErrInvalidRule) {
			t.Fatalf("%s: expected invalid rule, got %v", rule.Name, err)
		}
	}
	if err := engine.Run(context.Background(), []Rule{
		{Name: "same", Pattern: "a"},
		{Name: "same", Pattern: "b"},
	}); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("expected duplicate rule error, got %v", err)
	}
}

func hasEvent(events []Event, kind EventType) bool {
	for _, event := range events {
		if event.Type == kind {
			return true
		}
	}
	return false
}

// fakeStream is a deterministic full-duplex in-memory stream. Read blocks until
// input is fed or the stream is closed.
type fakeStream struct {
	mu     sync.Mutex
	input  []byte
	output bytes.Buffer
	notify chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		notify: make(chan struct{}, 1),
		closed: make(chan struct{}),
	}
}

func (s *fakeStream) Feed(data string) {
	s.mu.Lock()
	s.input = append(s.input, data...)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *fakeStream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.input) > 0 {
			n := copy(p, s.input)
			s.input = s.input[n:]
			s.mu.Unlock()
			return n, nil
		}
		s.mu.Unlock()

		select {
		case <-s.notify:
		case <-s.closed:
			return 0, io.EOF
		}
	}
}

func (s *fakeStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.Write(p)
}

func (s *fakeStream) Close() error {
	s.once.Do(func() {
		close(s.closed)
	})
	return nil
}

func (s *fakeStream) Output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}
