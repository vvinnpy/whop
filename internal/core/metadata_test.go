package core

import (
	"testing"
	"time"

	"ttydeck/internal/sshconfig"
	"ttydeck/internal/store"
)

func TestApplyMetadata(t *testing.T) {
	hosts := []sshconfig.Host{
		{Alias: "prod-db", Tags: []string{"config-tag"}},
		{Alias: "staging"},
	}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	metas := map[string]store.Meta{
		"prod-db": {
			Favorite:        true,
			Notes:           "primary",
			Tags:            []string{"db", "config-tag"},
			LastConnectedAt: at,
			ConnectCount:    3,
		},
	}

	got := ApplyMetadata(hosts, metas)
	if !got[0].Favorite || got[0].Notes != "primary" || got[0].ConnectCount != 3 || !got[0].LastConnectedAt.Equal(at) {
		t.Fatalf("metadata not applied: %+v", got[0])
	}
	if len(got[0].Tags) != 2 || got[0].Tags[0] != "config-tag" || got[0].Tags[1] != "db" {
		t.Fatalf("tags not merged: %#v", got[0].Tags)
	}
	if got[1].Favorite || got[1].Notes != "" {
		t.Fatalf("unexpected metadata on second host: %+v", got[1])
	}
}

func TestSortHostsFavoritesThenRecent(t *testing.T) {
	now := time.Now()
	hosts := []sshconfig.Host{
		{Alias: "zeta", LastConnectedAt: now.Add(-time.Hour)},
		{Alias: "beta"},
		{Alias: "alpha", Favorite: true},
		{Alias: "gamma", LastConnectedAt: now},
		{Alias: "delta", LastConnectedAt: now.Add(-2 * time.Hour)},
	}
	got := SortHosts(hosts)
	want := []string{"alpha", "gamma", "zeta", "delta", "beta"}
	if !equalStrings(aliases(got), want) {
		t.Fatalf("got %#v, want %#v", aliases(got), want)
	}
}
