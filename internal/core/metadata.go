package core

import (
	"sort"

	"whop/internal/sshconfig"
	"whop/internal/store"
)

// ApplyMetadata merges local metadata into config hosts.
func ApplyMetadata(hosts []sshconfig.Host, metas map[string]store.Meta) []sshconfig.Host {
	out := make([]sshconfig.Host, len(hosts))
	for i, host := range hosts {
		meta := metas[host.Alias]
		host.Favorite = meta.Favorite
		host.Notes = meta.Notes
		host.LastConnectedAt = meta.LastConnectedAt
		host.ConnectCount = meta.ConnectCount
		host.Record = meta.Record
		host.AccessMode = sshconfig.AccessModeFromOverride(meta.LoginOverride)
		tags := make([]string, 0, len(host.Tags)+len(meta.Tags))
		tags = append(tags, host.Tags...)
		tags = append(tags, meta.Tags...)
		host.Tags = store.SortTags(tags)
		out[i] = host
	}
	return out
}

// SortHosts orders favorites first, then recently connected hosts. Ties keep
// the original SSH config order.
func SortHosts(hosts []sshconfig.Host) []sshconfig.Host {
	out := append([]sshconfig.Host(nil), hosts...)
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.Favorite != right.Favorite {
			return left.Favorite
		}
		leftHas := !left.LastConnectedAt.IsZero()
		rightHas := !right.LastConnectedAt.IsZero()
		if leftHas != rightHas {
			return leftHas
		}
		if leftHas && !left.LastConnectedAt.Equal(right.LastConnectedAt) {
			return left.LastConnectedAt.After(right.LastConnectedAt)
		}
		return false
	})
	return out
}
