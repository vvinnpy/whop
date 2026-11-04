package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ttydeck/internal/nccm"
	"ttydeck/internal/sshconfig"
)

type importReport struct {
	Source   string   `json:"source"`
	Config   string   `json:"config"`
	Applied  bool     `json:"applied"`
	Added    []string `json:"added"`
	Skipped  []string `json:"skipped"`
	Warnings []string `json:"warnings"`
	Backup   string   `json:"backup,omitempty"`
}

func runImport(args []string, deps Deps) int {
	if len(args) == 0 || args[0] != "nccm" {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck import nccm [flags]")
		return 2
	}
	fs := newFlagSet("ttydeck import nccm", deps)
	source := fs.String("file", "", "path to nccm.yml (default: search standard locations)")
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	only := fs.String("only", "", "comma-separated nccm connection names")
	apply := fs.Bool("apply", false, "write changes (default is dry-run)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck import nccm [flags]")
		return 2
	}
	sourcePath := strings.TrimSpace(*source)
	if sourcePath == "" {
		var err error
		sourcePath, err = nccm.DefaultPath()
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
	}
	cfg, err := nccm.Load(sourcePath)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	selected := parseOnlyNames(*only)
	imports, warnings := convertNCCMConnections(cfg.Connections, selected)
	if len(imports) == 0 && len(warnings) == 0 && len(selected) > 0 {
		warnings = append(warnings, "no nccm connections matched --only")
	}
	cfgPath := resolvePath(*configPath)
	existing, err := deps.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	imports, skipped := filterExistingImports(existing, imports)
	report := importReport{
		Source:   sourcePath,
		Config:   cfgPath,
		Applied:  *apply,
		Added:    []string{},
		Skipped:  append([]string{}, skipped...),
		Warnings: append([]string{}, warnings...),
	}
	if *apply {
		result, err := sshconfig.AddHosts(cfgPath, imports)
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		report.Added = append(report.Added, result.Added...)
		report.Skipped = append(report.Skipped, result.Skipped...)
		report.Backup = result.Backup
	} else {
		for _, item := range imports {
			report.Added = append(report.Added, item.Alias)
		}
	}
	sort.Strings(report.Added)
	sort.Strings(report.Skipped)
	sort.Strings(report.Warnings)
	if *asJSON {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	for _, alias := range report.Added {
		action := "add"
		if !*apply {
			action = "would add"
		}
		fmt.Fprintf(deps.Stdout, "%s\t%s\n", action, alias)
	}
	for _, item := range report.Skipped {
		fmt.Fprintf(deps.Stdout, "skip\t%s\n", item)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(deps.Stderr, "warning: %s\n", warning)
	}
	if report.Backup != "" {
		fmt.Fprintf(deps.Stdout, "backup\t%s\n", report.Backup)
	}
	if !*apply {
		fmt.Fprintln(deps.Stdout, "dry-run: no files changed; use --apply to write")
	}
	return 0
}

func parseOnlyNames(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	result := make(map[string]bool)
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			result[name] = true
		}
	}
	return result
}

func normalizeNCCMAlias(name string) (string, string, bool) {
	alias := strings.TrimSpace(name)
	if strings.HasPrefix(alias, "#") {
		trimmed := strings.TrimSpace(strings.TrimLeft(alias, "#"))
		if trimmed == "" {
			return "", fmt.Sprintf("%s: invalid empty alias", name), false
		}
		return trimmed, fmt.Sprintf("%s: alias renamed to %s", name, trimmed), true
	}
	if strings.ContainsAny(alias, "*?![\\]") || strings.IndexFunc(alias, unicode.IsSpace) >= 0 {
		return "", fmt.Sprintf("%s: unsupported alias for ssh config; skipped", name), false
	}
	for _, value := range alias {
		if unicode.IsControl(value) {
			return "", fmt.Sprintf("%s: alias contains control characters; skipped", name), false
		}
	}
	return alias, "", true
}

func convertNCCMConnections(connections []nccm.Connection, selected map[string]bool) ([]sshconfig.HostImport, []string) {
	var imports []sshconfig.HostImport
	var warnings []string
	for _, connection := range connections {
		name := strings.TrimSpace(connection.Name)
		if len(selected) > 0 && !selected[name] {
			continue
		}
		alias, renameWarning, ok := normalizeNCCMAlias(name)
		if !ok {
			warnings = append(warnings, renameWarning)
			continue
		}
		if renameWarning != "" {
			warnings = append(warnings, renameWarning)
		}
		address := strings.TrimSpace(connection.Address)
		user := strings.TrimSpace(connection.User)
		if address == "" {
			if at := strings.LastIndex(name, "@"); at >= 0 {
				if user == "" {
					user = name[:at]
				}
				address = name[at+1:]
			} else {
				address = name
			}
		}
		if address == "" || strings.ContainsAny(address, " \t\r\n") {
			warnings = append(warnings, fmt.Sprintf("%s: invalid or empty address", name))
			continue
		}
		port := connection.Port
		if port == 0 {
			port = 22
		}
		analysis := analyzeNCCMCustomArgs(connection.CustomArgs)
		if analysis.port > 0 {
			port = analysis.port
		}
		if analysis.passwordMode {
			warnings = append(warnings, fmt.Sprintf("%s: nccm password was not imported; ttydeck will prompt interactively", name))
		}
		if len(analysis.unsupported) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: unsupported customargs ignored: %s", name, strings.Join(analysis.unsupported, ", ")))
		}
		imports = append(imports, sshconfig.HostImport{
			Alias:               alias,
			HostName:            address,
			User:                user,
			IdentityFile:        strings.TrimSpace(connection.Identity),
			Comment:             strings.TrimSpace(connection.Comment),
			Port:                port,
			ServerAliveInterval: connection.Keepalive,
			Compression:         analysis.compression,
			LegacySSHRSA:        analysis.legacySSHRSA,
		})
	}
	return imports, warnings
}

type nccmCustomArgs struct {
	port         int
	compression  bool
	legacySSHRSA bool
	passwordMode bool
	unsupported  []string
}

func analyzeNCCMCustomArgs(raw string) nccmCustomArgs {
	var result nccmCustomArgs
	fields := strings.Fields(raw)
	for index := 0; index < len(fields); index++ {
		switch fields[index] {
		case "-P":
			result.passwordMode = true
			if index+1 < len(fields) {
				index++
			}
		case "-z":
			if index+1 >= len(fields) {
				result.unsupported = append(result.unsupported, "-z")
				continue
			}
			index++
			switch fields[index] {
			case "ssh":
			case "ssh-rsa":
				result.legacySSHRSA = true
			case "expect":
				result.passwordMode = true
			case "expect-ssh-rsa":
				result.passwordMode = true
				result.legacySSHRSA = true
			default:
				result.unsupported = append(result.unsupported, "-z")
			}
		case "-C":
			result.compression = true
		case "-p":
			if index+1 < len(fields) {
				index++
				port, err := strconv.Atoi(fields[index])
				if err == nil && port > 0 && port <= 65535 {
					result.port = port
				} else {
					result.unsupported = append(result.unsupported, "-p")
				}
			} else {
				result.unsupported = append(result.unsupported, "-p")
			}
		default:
			flagValue := fields[index]
			if strings.HasPrefix(flagValue, "-") {
				result.unsupported = append(result.unsupported, flagValue)
			}
		}
	}
	return result
}

func filterExistingImports(existing []sshconfig.Host, imports []sshconfig.HostImport) ([]sshconfig.HostImport, []string) {
	byAlias := make(map[string]sshconfig.Host, len(existing))
	byEndpoint := make(map[string]string, len(existing))
	for _, host := range existing {
		byAlias[host.Alias] = host
		key := hostEndpointKey(host.HostName, host.User, host.Port)
		if _, exists := byEndpoint[key]; !exists {
			byEndpoint[key] = host.Alias
		}
	}
	seenEndpoints := make(map[string]string)
	var kept []sshconfig.HostImport
	var skipped []string
	for _, item := range imports {
		if _, ok := byAlias[item.Alias]; ok {
			skipped = append(skipped, item.Alias+" (alias already exists)")
			continue
		}
		port := item.Port
		if port == 0 {
			port = 22
		}
		key := hostEndpointKey(item.HostName, item.User, port)
		if alias, ok := byEndpoint[key]; ok {
			skipped = append(skipped, fmt.Sprintf("%s (same endpoint as %s)", item.Alias, alias))
			continue
		}
		if alias, ok := seenEndpoints[key]; ok {
			skipped = append(skipped, fmt.Sprintf("%s (duplicate endpoint of %s)", item.Alias, alias))
			continue
		}
		seenEndpoints[key] = item.Alias
		kept = append(kept, item)
	}
	return kept, skipped
}

func hostEndpointKey(host, user string, port int) string {
	return strings.ToLower(strings.TrimSpace(host)) + "\x00" +
		strings.TrimSpace(user) + "\x00" + strconv.Itoa(port)
}
