package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecordCommandSetsAndReadsPolicy(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"record", "prod-db", "on"}, td.deps); code != 0 {
		t.Fatalf("set exit code = %d, stderr=%q", code, td.stderr.String())
	}
	if !strings.Contains(td.stdout.String(), "prod-db recording=on") {
		t.Fatalf("set stdout = %q", td.stdout.String())
	}
	td.stdout.Reset()
	if code := Run([]string{"record", "-json", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("get exit code = %d, stderr=%q", code, td.stderr.String())
	}
	var status recordingStatus
	if err := json.Unmarshal(td.stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Alias != "prod-db" || !status.Recording {
		t.Fatalf("status = %#v", status)
	}
}

func TestRecordCommandRejectsBadValue(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"record", "prod-db", "maybe"}, td.deps); code != 2 {
		t.Fatalf("exit code = %d, stderr=%q", code, td.stderr.String())
	}
}

func TestConnectUsesPerHostRecordingPolicy(t *testing.T) {
	td := newTestDeps()
	if err := td.metaStore.SetRecord("prod-db", true); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"connect", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, td.stderr.String())
	}
	if !td.recorded {
		t.Fatal("connect did not honor per-host recording policy")
	}
}
