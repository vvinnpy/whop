// Package automation provides in-process expect rules and macro execution for
// terminal streams. It never spawns external programs and can be used with any
// io.ReadWriter, including SSH, serial, or in-memory test streams.
package automation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"
)

var (
	// ErrTimeout means an expect rule or the overall automation run timed out.
	ErrTimeout = errors.New("automation: timeout")
	// ErrSecretAlreadySent prevents a secret rule from being sent more than once.
	ErrSecretAlreadySent = errors.New("automation: secret already sent")
	// ErrStreamClosed means the underlying stream ended before a rule matched.
	ErrStreamClosed = errors.New("automation: stream closed")
	// ErrBufferLimit means unmatched output exceeded the configured safety limit.
	ErrBufferLimit = errors.New("automation: match buffer limit exceeded")
	// ErrInvalidRule means an expect rule failed validation.
	ErrInvalidRule = errors.New("automation: invalid rule")
)

// EventType identifies an audit event emitted by the automation engine.
type EventType string

const (
	EventRuleWaiting    EventType = "rule_waiting"
	EventRuleRetry      EventType = "rule_retry"
	EventRuleMatched    EventType = "rule_matched"
	EventRuleSent       EventType = "rule_sent"
	EventRuleTimeout    EventType = "rule_timeout"
	EventSecretBlocked  EventType = "secret_blocked"
	EventCanceled       EventType = "canceled"
	EventStreamClosed   EventType = "stream_closed"
	EventCompleted      EventType = "completed"
	EventMacroStep      EventType = "macro_step"
	EventMacroStepDone  EventType = "macro_step_done"
	EventMacroCompleted EventType = "macro_completed"
)

// Event is an audit-friendly automation event. It deliberately never contains
// matched output or sent data because either may contain credentials.
type Event struct {
	Time    time.Time
	Type    EventType
	Rule    string
	Macro   string
	Step    string
	Attempt int
	Bytes   int
	Message string
}

// EventFunc receives automation events synchronously. It must not mutate the
// engine or block indefinitely.
type EventFunc func(Event)

// Rule describes one ordered expect step. Timeout applies to each matching
// attempt. MaxRetries is the number of additional attempts after the first
// timeout. Secret rules are one-shot for the lifetime of an Engine, regardless
// of retries.
type Rule struct {
	Name       string
	Pattern    string
	Send       []byte
	Timeout    time.Duration
	MaxRetries int
	Secret     bool
}

// Options controls an Engine. OverallTimeout covers an entire Run or macro
// execution. CloseStream makes Engine.Close close the stream; setting a read
// deadline is still attempted first so deadline-aware streams can be stopped.
type Options struct {
	OverallTimeout time.Duration
	MaxBufferBytes int
	CloseStream    bool
	OnEvent        EventFunc
}

const defaultMaxBufferBytes = 1 << 20

// Engine executes ordered expect rules against a single stream. An Engine
// preserves unread output between Expect calls, which allows a macro runner to
// interleave waits and sends without losing bytes. Engine is safe for serial
// use; concurrent operations are serialized.
type Engine struct {
	stream io.ReadWriter
	opts   Options

	mu      sync.Mutex
	pending []byte
	pump    *streamPump
	secrets map[string]struct{}

	closeOnce sync.Once
	closeErr  error
}

// NewEngine creates an automation engine over stream. Call Close when finished
// so the internal reader can be released.
func NewEngine(stream io.ReadWriter, opts Options) (*Engine, error) {
	if stream == nil {
		return nil, errors.New("automation: nil stream")
	}
	if opts.OverallTimeout < 0 {
		return nil, fmt.Errorf("%w: negative overall timeout", ErrInvalidRule)
	}
	if opts.MaxBufferBytes < 0 {
		return nil, fmt.Errorf("%w: negative buffer limit", ErrInvalidRule)
	}
	if opts.MaxBufferBytes == 0 {
		opts.MaxBufferBytes = defaultMaxBufferBytes
	}
	return &Engine{
		stream:  stream,
		opts:    opts,
		secrets: make(map[string]struct{}),
	}, nil
}

// Run executes rules in declaration order. Every rule must match. Overall
// timeout, when configured, applies to the complete sequence.
func (e *Engine) Run(ctx context.Context, rules []Rule) error {
	if ctx == nil {
		return errors.New("automation: nil context")
	}
	compiled, err := compileRules(rules)
	if err != nil {
		return err
	}

	runCtx, cancel := e.withOverallTimeout(ctx)
	defer cancel()

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, rule := range compiled {
		if err := e.expectLocked(runCtx, rule); err != nil {
			return wrapOverallError(runCtx, err)
		}
	}
	e.emit(Event{Type: EventCompleted, Message: fmt.Sprintf("%d rules completed", len(compiled))})
	return nil
}

// Expect waits for one rule and sends its response. It applies the rule timeout
// and retry policy. Callers can enforce an overall deadline through ctx.
func (e *Engine) Expect(ctx context.Context, rule Rule) error {
	if ctx == nil {
		return errors.New("automation: nil context")
	}
	compiled, err := compileRules([]Rule{rule})
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	return e.expectLocked(ctx, compiled[0])
}

// Send writes data and emits an audit event. It does not open the stream reader.
func (e *Engine) Send(ctx context.Context, name string, data []byte) error {
	if ctx == nil {
		return errors.New("automation: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sendLocked(name, "", data, false)
}

// SendOnce writes data at most once for the supplied name. It is used by macro
// secret steps so repeated execution can never resend a password or token.
func (e *Engine) SendOnce(ctx context.Context, name string, data []byte) error {
	if ctx == nil {
		return errors.New("automation: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sendLocked(name, "send:"+name, data, true)
}

// Close stops the stream reader. Deadline-aware streams are interrupted by a
// read deadline; otherwise CloseStream controls whether the stream is closed.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		pump := e.pump
		e.pump = nil
		e.mu.Unlock()

		if pump != nil {
			pump.stop()
		} else if e.opts.CloseStream {
			if closer, ok := e.stream.(io.Closer); ok {
				e.closeErr = closer.Close()
			}
		}
	})
	return e.closeErr
}

type compiledRule struct {
	rule      Rule
	re        *regexp.Regexp
	display   string
	secretKey string
}

func compileRules(rules []Rule) ([]compiledRule, error) {
	compiled := make([]compiledRule, 0, len(rules))
	names := make(map[string]struct{}, len(rules))
	for i, rule := range rules {
		display := rule.Name
		if display == "" {
			display = fmt.Sprintf("rule-%d", i+1)
		}
		if rule.Name != "" {
			if _, exists := names[rule.Name]; exists {
				return nil, fmt.Errorf("%w: duplicate rule name %q", ErrInvalidRule, rule.Name)
			}
			names[rule.Name] = struct{}{}
		}
		if rule.Pattern == "" {
			return nil, fmt.Errorf("%w: %s has empty pattern", ErrInvalidRule, display)
		}
		if rule.Timeout < 0 {
			return nil, fmt.Errorf("%w: %s has negative timeout", ErrInvalidRule, display)
		}
		if rule.MaxRetries < 0 {
			return nil, fmt.Errorf("%w: %s has negative retries", ErrInvalidRule, display)
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %s pattern: %v", ErrInvalidRule, display, err)
		}
		if re.MatchString("") {
			return nil, fmt.Errorf("%w: %s pattern matches empty input", ErrInvalidRule, display)
		}
		key := rule.Name
		if key == "" {
			key = rule.Pattern
		}
		compiled = append(compiled, compiledRule{
			rule:      rule,
			re:        re,
			display:   display,
			secretKey: "rule:" + key,
		})
	}
	return compiled, nil
}

func (e *Engine) withOverallTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.opts.OverallTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, e.opts.OverallTimeout)
}

func wrapOverallError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: overall timeout: %v", ErrTimeout, ctx.Err())
	}
	return err
}

func (e *Engine) expectLocked(ctx context.Context, rule compiledRule) error {
	if ctx == nil {
		return errors.New("automation: nil context")
	}
	if rule.rule.Secret {
		if _, sent := e.secrets[rule.secretKey]; sent {
			e.emit(Event{Type: EventSecretBlocked, Rule: rule.display, Message: "secret rule already sent"})
			return fmt.Errorf("%w: %s", ErrSecretAlreadySent, rule.display)
		}
	}

	attempts := rule.rule.MaxRetries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		e.emit(Event{Type: EventRuleWaiting, Rule: rule.display, Attempt: attempt})
		err := e.waitForRuleLocked(ctx, rule, attempt)
		if err == nil {
			return nil
		}
		if errors.Is(err, errAttemptTimeout) {
			e.emit(Event{Type: EventRuleTimeout, Rule: rule.display, Attempt: attempt})
			if attempt < attempts {
				e.emit(Event{Type: EventRuleRetry, Rule: rule.display, Attempt: attempt + 1})
				continue
			}
			return fmt.Errorf("%w: rule %q after %d attempt(s)", ErrTimeout, rule.display, attempts)
		}
		return err
	}
	panic("unreachable")
}

var errAttemptTimeout = errors.New("automation: attempt timeout")

func (e *Engine) waitForRuleLocked(ctx context.Context, rule compiledRule, attempt int) error {
	if matched, err := e.tryMatchLocked(rule, attempt); matched || err != nil {
		return err
	}
	if e.pump == nil {
		e.pump = newStreamPump(e.stream, e.opts.CloseStream)
	}

	var timer *time.Timer
	var timeout <-chan time.Time
	if rule.rule.Timeout > 0 {
		timer = time.NewTimer(rule.rule.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			e.emit(Event{Type: EventCanceled, Rule: rule.display, Attempt: attempt, Message: ctx.Err().Error()})
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
			}
			return ctx.Err()
		case <-timeout:
			return errAttemptTimeout
		case result, ok := <-e.pump.results:
			if !ok {
				err := e.pump.terminalError()
				e.emit(Event{Type: EventStreamClosed, Rule: rule.display, Attempt: attempt, Message: err.Error()})
				return fmt.Errorf("%w: %v", ErrStreamClosed, err)
			}
			e.pending = append(e.pending, result...)
			if matched, err := e.tryMatchLocked(rule, attempt); matched || err != nil {
				return err
			}
			if len(e.pending) > e.opts.MaxBufferBytes {
				return fmt.Errorf("%w: %d bytes", ErrBufferLimit, len(e.pending))
			}
		}
	}
}

func (e *Engine) tryMatchLocked(rule compiledRule, attempt int) (bool, error) {
	loc := rule.re.FindIndex(e.pending)
	if loc == nil {
		return false, nil
	}
	e.pending = e.pending[loc[1]:]
	e.emit(Event{Type: EventRuleMatched, Rule: rule.display, Attempt: attempt, Bytes: loc[1] - loc[0]})
	// Reserve a secret before writing so a partial write or stream error can
	// never cause the same credential to be sent again.
	if rule.rule.Secret {
		e.secrets[rule.secretKey] = struct{}{}
	}
	if len(rule.rule.Send) > 0 {
		if err := e.writeAllLocked(rule.rule.Send); err != nil {
			e.emit(Event{Type: EventRuleSent, Rule: rule.display, Attempt: attempt, Message: err.Error()})
			return true, fmt.Errorf("automation: write response for %s: %w", rule.display, err)
		}
	}
	e.emit(Event{Type: EventRuleSent, Rule: rule.display, Attempt: attempt, Bytes: len(rule.rule.Send)})
	return true, nil
}

func (e *Engine) sendLocked(name, secretKey string, data []byte, secret bool) error {
	if secret {
		if _, sent := e.secrets[secretKey]; sent {
			e.emit(Event{Type: EventSecretBlocked, Rule: name, Message: "secret already sent"})
			return fmt.Errorf("%w: %s", ErrSecretAlreadySent, name)
		}
	}
	if secret {
		e.secrets[secretKey] = struct{}{}
	}
	if err := e.writeAllLocked(data); err != nil {
		e.emit(Event{Type: EventRuleSent, Rule: name, Bytes: len(data), Message: err.Error()})
		return err
	}
	e.emit(Event{Type: EventRuleSent, Rule: name, Bytes: len(data)})
	return nil
}

func (e *Engine) writeAllLocked(data []byte) error {
	for len(data) > 0 {
		n, err := e.stream.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (e *Engine) emit(event Event) {
	if e.opts.OnEvent == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	e.opts.OnEvent(event)
}

type streamPump struct {
	stream      io.ReadWriter
	closeOnStop bool
	results     chan []byte
	stopCh      chan struct{}
	stopOnce    sync.Once
	terminal    error
}

func newStreamPump(stream io.ReadWriter, closeOnStop bool) *streamPump {
	pump := &streamPump{
		stream:      stream,
		closeOnStop: closeOnStop,
		results:     make(chan []byte, 16),
		stopCh:      make(chan struct{}),
	}
	go pump.run()
	return pump
}

func (p *streamPump) run() {
	defer close(p.results)
	buf := make([]byte, 4096)
	for {
		n, err := p.stream.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			select {
			case p.results <- data:
			case <-p.stopCh:
				return
			}
		}
		if err != nil {
			p.terminal = err
			return
		}
		if n == 0 {
			select {
			case <-p.stopCh:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}
}

func (p *streamPump) terminalError() error {
	if p.terminal != nil {
		return p.terminal
	}
	return io.EOF
}

func (p *streamPump) stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		if deadline, ok := p.stream.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = deadline.SetReadDeadline(time.Now())
		}
		if p.closeOnStop {
			if closer, ok := p.stream.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	})
}
