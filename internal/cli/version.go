package cli

import (
	"runtime/debug"
	"strings"
)

// versionString prefers a release tag injected at build time. Otherwise it
// uses Go's VCS-stamped module version: an exact tag at tagged commits, a
// canonical pseudo-version after a tag, or v0.0.0-<timestamp>-<revision> in an
// untagged repository.
func versionString() string {
	if version := strings.TrimSpace(Version); version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}
	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	version := "dev+" + revision
	if modified {
		version += ".dirty"
	}
	return version
}
