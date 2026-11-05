package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
)

func TestForwardCommandStartsLocalAndDynamic(t *testing.T) {
	td := newTestDeps()
	var mu sync.Mutex
	var specs []coreconnector.ForwardSpec
	var started sync.WaitGroup
	started.Add(2)
	td.deps.Forward = func(_ context.Context, host sshconfig.Host, _ []sshconfig.Host, spec coreconnector.ForwardSpec, _ bool, ready func(coreconnector.ForwardStatus) error) error {
		mu.Lock()
		specs = append(specs, spec)
		mu.Unlock()
		status := coreconnector.ForwardStatus{Kind: spec.Kind, ListenAddress: spec.BindAddress()}
		if spec.Kind != coreconnector.ForwardDynamic {
			status.DestinationAddress = spec.DestinationAddress()
		}
		if err := ready(status); err != nil {
			return err
		}
		started.Done()
		started.Wait()
		return nil
	}
	code := Run([]string{"forward", "-L", "8080:db:5432", "-D", "1080", "prod-db"}, td.deps)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, td.stderr.String())
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %#v", specs)
	}
	byKind := make(map[coreconnector.ForwardKind]coreconnector.ForwardSpec, len(specs))
	for _, spec := range specs {
		byKind[spec.Kind] = spec
	}
	if local := byKind[coreconnector.ForwardLocal]; local.DestinationHost != "db" {
		t.Fatalf("local spec = %#v", local)
	}
	if dynamic := byKind[coreconnector.ForwardDynamic]; dynamic.BindPort != 1080 {
		t.Fatalf("dynamic spec = %#v", dynamic)
	}
	if !strings.Contains(td.stdout.String(), "SOCKS5") || !strings.Contains(td.stdout.String(), "db:5432") {
		t.Fatalf("stdout = %q", td.stdout.String())
	}
}

func TestForwardCommandJSONStatus(t *testing.T) {
	td := newTestDeps()
	td.deps.Forward = func(_ context.Context, _ sshconfig.Host, _ []sshconfig.Host, spec coreconnector.ForwardSpec, _ bool, ready func(coreconnector.ForwardStatus) error) error {
		return ready(coreconnector.ForwardStatus{Kind: spec.Kind, ListenAddress: "127.0.0.1:8080", DestinationAddress: "db:5432"})
	}
	if code := Run([]string{"forward", "-json", "-L", "8080:db:5432", "prod-db"}, td.deps); code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, td.stderr.String())
	}
	var status coreconnector.ForwardStatus
	if err := json.Unmarshal([]byte(strings.TrimSpace(td.stdout.String())), &status); err != nil {
		t.Fatalf("decode status: %v (%q)", err, td.stdout.String())
	}
	if status.Kind != coreconnector.ForwardLocal || status.ListenAddress != "127.0.0.1:8080" {
		t.Fatalf("status = %#v", status)
	}
}

func TestForwardCommandRejectsBadSpec(t *testing.T) {
	td := newTestDeps()
	if code := Run([]string{"forward", "-L", "8080:db", "prod-db"}, td.deps); code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, td.stderr.String())
	}
}
