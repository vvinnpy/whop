package automation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// StepKind identifies a macro step.
type StepKind string

const (
	StepSend  StepKind = "send"
	StepWait  StepKind = "wait"
	StepSleep StepKind = "sleep"
)

// Step is one operation in a macro. Send writes bytes, Wait matches a regex,
// and Sleep pauses execution. Secret sends are one-shot within one macro run.
type Step struct {
	Name       string
	Kind       StepKind
	Send       []byte
	Pattern    string
	Timeout    time.Duration
	MaxRetries int
	Duration   time.Duration
	Secret     bool
}

// Macro is a named sequence of send, wait, and sleep steps.
type Macro struct {
	Name  string
	Steps []Step
}

// ErrInvalidMacro means a macro failed validation.
var ErrInvalidMacro = errors.New("automation: invalid macro")

// Validate checks the complete macro before any bytes are written.
func (m Macro) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidMacro)
	}
	if len(m.Steps) == 0 {
		return fmt.Errorf("%w: %s has no steps", ErrInvalidMacro, m.Name)
	}
	secretNames := make(map[string]struct{})
	for i, step := range m.Steps {
		stepName := macroStepName(m.Name, i, step.Name)
		switch step.Kind {
		case StepSend:
			if len(step.Send) == 0 {
				return fmt.Errorf("%w: %s step %q has no data", ErrInvalidMacro, m.Name, stepName)
			}
			if step.Pattern != "" || step.Timeout != 0 || step.MaxRetries != 0 || step.Duration != 0 {
				return fmt.Errorf("%w: %s send step %q has irrelevant fields", ErrInvalidMacro, m.Name, stepName)
			}
			if step.Secret {
				key := macroSecretKey(m.Name, stepName)
				if _, exists := secretNames[key]; exists {
					return fmt.Errorf("%w: duplicate secret step %q", ErrInvalidMacro, stepName)
				}
				secretNames[key] = struct{}{}
			}
		case StepWait:
			if step.Pattern == "" {
				return fmt.Errorf("%w: %s wait step %q has no pattern", ErrInvalidMacro, m.Name, stepName)
			}
			if step.Timeout < 0 {
				return fmt.Errorf("%w: %s wait step %q has negative timeout", ErrInvalidMacro, m.Name, stepName)
			}
			if step.MaxRetries < 0 {
				return fmt.Errorf("%w: %s wait step %q has negative retries", ErrInvalidMacro, m.Name, stepName)
			}
			if len(step.Send) != 0 || step.Duration != 0 || step.Secret {
				return fmt.Errorf("%w: %s wait step %q has irrelevant fields", ErrInvalidMacro, m.Name, stepName)
			}
			if _, err := compileRules([]Rule{{Name: stepName, Pattern: step.Pattern}}); err != nil {
				return fmt.Errorf("%w: %s wait step %q: %v", ErrInvalidMacro, m.Name, stepName, err)
			}
		case StepSleep:
			if step.Duration <= 0 {
				return fmt.Errorf("%w: %s sleep step %q needs a positive duration", ErrInvalidMacro, m.Name, stepName)
			}
			if step.Pattern != "" || step.Timeout != 0 || step.MaxRetries != 0 || len(step.Send) != 0 || step.Secret {
				return fmt.Errorf("%w: %s sleep step %q has irrelevant fields", ErrInvalidMacro, m.Name, stepName)
			}
		default:
			return fmt.Errorf("%w: %s step %q has unknown kind %q", ErrInvalidMacro, m.Name, stepName, step.Kind)
		}
	}
	return nil
}

// RunMacro executes a validated macro using an expect Engine. The same engine
// is reused for every wait step so bytes read ahead by one rule remain available
// to the next rule.
func RunMacro(ctx context.Context, stream io.ReadWriter, macro Macro, opts Options) error {
	if err := macro.Validate(); err != nil {
		return err
	}
	engine, err := NewEngine(stream, opts)
	if err != nil {
		return err
	}
	defer engine.Close()

	runCtx, cancel := engine.withOverallTimeout(ctx)
	defer cancel()

	for i, step := range macro.Steps {
		stepName := macroStepName(macro.Name, i, step.Name)
		engine.emit(Event{Type: EventMacroStep, Macro: macro.Name, Step: stepName, Message: string(step.Kind)})

		switch step.Kind {
		case StepSend:
			if step.Secret {
				err = engine.SendOnce(runCtx, stepName, step.Send)
			} else {
				err = engine.Send(runCtx, stepName, step.Send)
			}
		case StepWait:
			err = engine.Expect(runCtx, Rule{
				Name:       stepName,
				Pattern:    step.Pattern,
				Timeout:    step.Timeout,
				MaxRetries: step.MaxRetries,
			})
		case StepSleep:
			err = sleepContext(runCtx, step.Duration)
		}
		if err != nil {
			return wrapOverallError(runCtx, fmt.Errorf("automation: macro %q step %d (%s): %w", macro.Name, i+1, stepName, err))
		}
		engine.emit(Event{Type: EventMacroStepDone, Macro: macro.Name, Step: stepName, Message: string(step.Kind)})
	}
	engine.emit(Event{Type: EventMacroCompleted, Macro: macro.Name, Message: fmt.Sprintf("%d steps completed", len(macro.Steps))})
	return nil
}

func macroStepName(macro string, index int, name string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return fmt.Sprintf("%s/step-%d", macro, index+1)
}

func macroSecretKey(macro, step string) string {
	return "macro:" + macro + ":" + step
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
