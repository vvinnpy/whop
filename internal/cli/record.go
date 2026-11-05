package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"whop/internal/sshconfig"
)

type recordingStatus struct {
	Alias     string `json:"alias"`
	Recording bool   `json:"recording"`
}

func runRecord(args []string, deps Deps) int {
	fs := newFlagSet("whop record", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop record [flags] <alias> [on|off]")
		return 2
	}
	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	host, ok := sshconfig.Find(hosts, fs.Arg(0))
	if !ok {
		fmt.Fprintf(deps.Stderr, "whop: host %q not found\n", fs.Arg(0))
		return 1
	}
	if fs.NArg() == 2 {
		enabled, ok := parseOnOff(fs.Arg(1))
		if !ok {
			fmt.Fprintln(deps.Stderr, "usage: recording value must be on/off")
			return 2
		}
		metaStore, err := deps.OpenStore()
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		defer metaStore.Close()
		if err := metaStore.SetRecord(host.Alias, enabled); err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		host.Record = enabled
	}
	status := recordingStatus{Alias: host.Alias, Recording: host.Record}
	if *asJSON {
		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	value := "off"
	if status.Recording {
		value = "on"
	}
	fmt.Fprintf(deps.Stdout, "%s recording=%s\n", status.Alias, value)
	return 0
}

func parseOnOff(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1":
		return true, true
	case "off", "false", "no", "0":
		return false, true
	default:
		return false, false
	}
}
