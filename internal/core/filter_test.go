package core

import (
	"testing"

	"ttydeck/internal/sshconfig"
)

func sampleHosts() []sshconfig.Host {
	return []sshconfig.Host{
		{Alias: "prod-db", HostName: "10.0.0.11", User: "deploy", Port: 22, Tags: []string{"prod", "db"}},
		{Alias: "prod-cache", HostName: "10.0.0.12", User: "deploy", Port: 2222, Tags: []string{"prod"}},
		{Alias: "staging-db", HostName: "10.1.0.11", User: "admin", Port: 22, Tags: []string{"staging", "db"}},
		{Alias: "github.com", HostName: "github.com", User: "git", Port: 22, Kind: sshconfig.KindGitAuth},
	}
}

func aliases(hosts []sshconfig.Host) []string {
	out := make([]string, len(hosts))
	for i, host := range hosts {
		out[i] = host.Alias
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilterEmptyReturnsCopy(t *testing.T) {
	hosts := sampleHosts()
	got := Filter(hosts, "")
	if !equalStrings(aliases(got), aliases(hosts)) {
		t.Fatalf("unexpected result: %#v", aliases(got))
	}
	got[0].Alias = "changed"
	if hosts[0].Alias != "prod-db" {
		t.Fatal("Filter must return a copy")
	}
}

func TestFilterANDTerms(t *testing.T) {
	got := Filter(sampleHosts(), "prod db")
	want := []string{"prod-db"}
	if !equalStrings(aliases(got), want) {
		t.Fatalf("got %#v, want %#v", aliases(got), want)
	}
}

func TestFilterIsCaseInsensitive(t *testing.T) {
	got := Filter(sampleHosts(), "PROD CACHE")
	want := []string{"prod-cache"}
	if !equalStrings(aliases(got), want) {
		t.Fatalf("got %#v, want %#v", aliases(got), want)
	}
}

func TestFilterFieldPrefixes(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"tag:db", []string{"prod-db", "staging-db"}},
		{"tag:staging user:admin", []string{"staging-db"}},
		{"host:10.0.0.1", []string{"prod-db", "prod-cache"}},
		{"user:git", []string{"github.com"}},
		{"2222", []string{"prod-cache"}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := Filter(sampleHosts(), tt.query)
			if !equalStrings(aliases(got), tt.want) {
				t.Fatalf("got %#v, want %#v", aliases(got), tt.want)
			}
		})
	}
}

func TestFilterNoMatch(t *testing.T) {
	got := Filter(sampleHosts(), "does-not-exist")
	if len(got) != 0 {
		t.Fatalf("expected no matches, got %#v", aliases(got))
	}
}
