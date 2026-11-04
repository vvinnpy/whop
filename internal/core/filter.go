// Package core contains connection-list use cases independent of the TUI.
package core

import (
	"strconv"
	"strings"

	"whop/internal/sshconfig"
)

// Filter applies nccm-style whitespace-separated AND filters.
//
// Supported field prefixes:
//
//	tag:prod
//	user:deploy
//	host:db
//
// A plain term matches alias, hostname, user, port, kind, and tags.
func Filter(hosts []sshconfig.Host, query string) []sshconfig.Host {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return append([]sshconfig.Host(nil), hosts...)
	}

	out := make([]sshconfig.Host, 0, len(hosts))
	for _, host := range hosts {
		if matchesAll(host, terms) {
			out = append(out, host)
		}
	}
	return out
}

func matchesAll(host sshconfig.Host, terms []string) bool {
	for _, term := range terms {
		if !matches(host, term) {
			return false
		}
	}
	return true
}

func matches(host sshconfig.Host, term string) bool {
	field, value, hasField := strings.Cut(term, ":")
	if hasField {
		switch field {
		case "tag":
			return contains(host.Tags, value)
		case "user":
			return containsFold(host.User, value)
		case "host":
			return containsFold(host.Alias, value) || containsFold(host.HostName, value)
		}
	}

	return containsFold(host.Alias, term) ||
		containsFold(host.HostName, term) ||
		containsFold(host.User, term) ||
		containsFold(strconv.Itoa(host.Port), term) ||
		containsFold(string(host.Kind), term) ||
		contains(host.Tags, term) ||
		containsFold(host.Notes, term)
}

func containsFold(value, term string) bool {
	return strings.Contains(strings.ToLower(value), term)
}

func contains(values []string, term string) bool {
	for _, value := range values {
		if strings.EqualFold(value, term) || containsFold(value, term) {
			return true
		}
	}
	return false
}
