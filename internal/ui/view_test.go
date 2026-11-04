package ui

import (
	"strings"
	"testing"

	"whop/internal/sshconfig"
)

func TestListColumnsShowTagsAtMediumWidth(t *testing.T) {
	tests := []struct {
		width    int
		wantTags bool
	}{
		{width: 100, wantTags: true},
		{width: 80, wantTags: true},
		{width: 72, wantTags: true},
		{width: 60, wantTags: false},
	}
	for _, tt := range tests {
		if got := listColumns(tt.width).showTags; got != tt.wantTags {
			t.Errorf("listColumns(%d).showTags = %v, want %v", tt.width, got, tt.wantTags)
		}
	}
}

func TestViewListShowsTags(t *testing.T) {
	hosts := []sshconfig.Host{
		{Alias: "prod-db", HostName: "10.0.0.11", Port: 22, Tags: []string{"prod", "db"}, Kind: sshconfig.KindServer},
		{Alias: "bare", HostName: "10.0.0.12", Port: 22, Kind: sshconfig.KindServer},
	}
	m, _ := newTestModel(t, hosts)

	lines := m.viewList(100, 30)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "TAGS") {
		t.Fatalf("missing TAGS header:\n%s", joined)
	}
	if !strings.Contains(joined, "prod,db") {
		t.Fatalf("missing tags for prod-db:\n%s", joined)
	}
	if !strings.Contains(joined, "-") {
		t.Fatalf("missing placeholder for empty tags:\n%s", joined)
	}

	lines = m.viewList(72, 30)
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "TAGS") || !strings.Contains(joined, "prod,db") {
		t.Fatalf("tags missing at medium width:\n%s", joined)
	}
}
