package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sync"
	"time"

	"ttydeck/internal/automation"
	sshconn "ttydeck/internal/connector/ssh"
	"ttydeck/internal/store"
)

type auditCollector struct {
	mu     sync.Mutex
	store  MetaStore
	events []store.AuditEvent
}

func newAuditCollector(metaStore MetaStore) *auditCollector {
	return &auditCollector{store: metaStore}
}

func (c *auditCollector) add(event store.AuditEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *auditCollector) automationEvent(host, macro string) automation.EventFunc {
	return func(event automation.Event) {
		name := event.Macro
		if name == "" {
			name = macro
		}
		c.add(store.AuditEvent{
			Time:    event.Time,
			Type:    string(event.Type),
			Host:    host,
			Macro:   name,
			Step:    event.Step,
			Rule:    event.Rule,
			Attempt: event.Attempt,
			Bytes:   event.Bytes,
			Message: event.Message,
		})
	}
}

func (c *auditCollector) flush() error {
	c.mu.Lock()
	events := append([]store.AuditEvent(nil), c.events...)
	c.events = nil
	c.mu.Unlock()
	for _, event := range events {
		if err := c.store.AppendAudit(event); err != nil {
			return err
		}
	}
	return nil
}

func runAudit(args []string, deps Deps) int {
	if len(args) > 0 && args[0] == "clear" {
		return runAuditClear(args[1:], deps)
	}
	fs := newFlagSet("ttydeck audit", deps)
	limit := fs.Int("limit", 100, "maximum events to show (max 1000)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck audit [flags] | ttydeck audit clear")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	events, err := metaStore.AuditEvents(*limit)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if *asJSON {
		if events == nil {
			events = []store.AuditEvent{}
		}
		encoded, err := json.MarshalIndent(events, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	for _, event := range events {
		message := sanitizeSingleLine(event.Message)
		fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
			event.Time.Local().Format("2006-01-02 15:04:05 -0700"),
			sanitizeSingleLine(event.Type),
			sanitizeSingleLine(event.Host),
			sanitizeSingleLine(event.Macro),
			sanitizeSingleLine(event.Step),
			event.Attempt,
			event.Bytes,
			message,
		)
	}
	return 0
}

func runAuditClear(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck audit clear", deps)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck audit clear [flags]")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if err := metaStore.ClearAudit(); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if *asJSON {
		fmt.Fprintln(deps.Stdout, `{"cleared":true}`)
	} else {
		fmt.Fprintln(deps.Stdout, "audit log cleared")
	}
	return 0
}

func macroAuditStarted(collector *auditCollector, host, macro string) {
	collector.add(store.AuditEvent{
		Time:  time.Now(),
		Type:  "macro_started",
		Host:  host,
		Macro: macro,
	})
}

func macroAuditFailed(collector *auditCollector, host, macro string, err error) {
	diagnostic := sshconn.Diagnose(host, err)
	message := diagnostic.Stage + "/" + diagnostic.Category
	if diagnostic.Stage == "" {
		message = diagnostic.Category
	}
	collector.add(store.AuditEvent{
		Time:    time.Now(),
		Type:    "macro_failed",
		Host:    host,
		Macro:   macro,
		Message: message,
	})
}

func macroAuditFlush(collector *auditCollector, stderrWriter io.Writer) error {
	if err := collector.flush(); err != nil {
		if stderrWriter != nil {
			_, _ = fmt.Fprintf(stderrWriter, "ttydeck: warning: write audit log: %v\n", err)
		}
		return err
	}
	return nil
}
