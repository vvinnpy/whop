package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditAppendAndOrdering(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	base := time.Date(2026, time.November, 4, 10, 0, 0, 0, time.UTC)
	events := []AuditEvent{
		{Time: base, Type: "macro_started", Host: "db-1", Macro: "deploy"},
		{Time: base.Add(time.Minute), Type: "step_succeeded", Host: "db-1", Macro: "deploy", Step: "wait-ready"},
		{Time: base.Add(time.Minute), Type: "step_failed", Host: "db-2", Macro: "deploy", Step: "wait-ready", Attempt: 2},
		{Time: base.Add(time.Minute).Add(500 * time.Millisecond), Type: "macro_completed", Host: "db-1", Macro: "deploy"},
	}
	for _, event := range events {
		if err := db.AppendAudit(event); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.AuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("events = %d, want %d", len(got), len(events))
	}
	if got[0].Type != "macro_completed" || got[1].Type != "step_failed" ||
		got[2].Type != "step_succeeded" || got[3].Type != "macro_started" {
		t.Fatalf("unexpected ordering: %#v", got)
	}
	if !got[3].Time.Equal(base) {
		t.Fatalf("time = %s, want %s", got[2].Time, base)
	}
}

func TestAuditZeroTimeGetsCurrentTime(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	before := time.Now().Add(-time.Second)
	if err := db.AppendAudit(AuditEvent{Type: "connection_opened"}); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(time.Second)

	events, err := db.AuditEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Time.Before(before) || events[0].Time.After(after) {
		t.Fatalf("default time %s is outside [%s, %s]", events[0].Time, before, after)
	}
}

func TestAuditEmptyType(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, eventType := range []string{"", " ", "\t\n"} {
		if err := db.AppendAudit(AuditEvent{Type: eventType}); err == nil {
			t.Fatalf("expected error for empty event type %q", eventType)
		}
	}
}

func TestAuditLimits(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	base := time.Date(2026, time.November, 4, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 1100; i++ {
		if err := db.AppendAudit(AuditEvent{
			Time: base.Add(time.Duration(i) * time.Second),
			Type: "event",
		}); err != nil {
			t.Fatal(err)
		}
	}

	defaulted, err := db.AuditEvents(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaulted) != defaultAuditEventLimit {
		t.Fatalf("default limit returned %d events, want %d", len(defaulted), defaultAuditEventLimit)
	}

	capped, err := db.AuditEvents(5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != maxAuditEventLimit {
		t.Fatalf("maximum limit returned %d events, want %d", len(capped), maxAuditEventLimit)
	}
	if !capped[0].Time.Equal(base.Add(1099 * time.Second)) {
		t.Fatalf("newest time = %s", capped[0].Time)
	}
}

func TestAuditPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.November, 4, 11, 12, 13, 123456789, time.UTC)
	if err := db.AppendAudit(AuditEvent{
		Time: at, Type: "macro_completed", Host: "prod-db", Macro: "deploy",
		Step: "verify", Rule: "ready", Attempt: 2, Bytes: 4096, Message: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	events, err := reopened.AuditEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	got := events[0]
	if !got.Time.Equal(at) || got.Type != "macro_completed" || got.Host != "prod-db" ||
		got.Macro != "deploy" || got.Step != "verify" || got.Rule != "ready" ||
		got.Attempt != 2 || got.Bytes != 4096 || got.Message != "completed" {
		t.Fatalf("event did not persist correctly: %#v", got)
	}
}

func TestAuditClear(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.AppendAudit(AuditEvent{Type: "event"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearAudit(); err != nil {
		t.Fatal(err)
	}
	events, err := db.AuditEvents(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events after clear = %#v", events)
	}
	if err := db.ClearAudit(); err != nil {
		t.Fatalf("second clear failed: %v", err)
	}
}

func TestAuditSpecialCharactersRoundTripWithoutContentFields(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	event := AuditEvent{
		Time:    time.Date(2026, time.November, 4, 12, 0, 0, 42, time.FixedZone("UTC+8", 8*60*60)),
		Type:    "rule_matched",
		Host:    "测试主机\n\"prod\"",
		Macro:   "deploy\t'blue'",
		Step:    "step|1",
		Rule:    `(?i)^ready\s+\d+%$`,
		Attempt: 3,
		Bytes:   1024,
		Message: "utf-8 中文 & <xml> 'quoted' \"double\" ; --",
	}
	if err := db.AppendAudit(event); err != nil {
		t.Fatal(err)
	}

	events, err := db.AuditEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	got := events[0]
	if !got.Time.Equal(event.Time) {
		t.Fatalf("time = %s, want %s", got.Time, event.Time)
	}
	if got.Type != event.Type || got.Host != event.Host || got.Macro != event.Macro ||
		got.Step != event.Step || got.Rule != event.Rule || got.Attempt != event.Attempt ||
		got.Bytes != event.Bytes || got.Message != event.Message {
		t.Fatalf("round trip changed event:\ngot:  %#v\nwant: %#v", got, event)
	}

	all := fmt.Sprintf("%#v", got)
	for _, contentField := range []string{"Command", "Output", "Payload", "Transcript", "Password"} {
		if strings.Contains(all, contentField) {
			t.Fatalf("audit event unexpectedly exposes content field %q: %s", contentField, all)
		}
	}
}
