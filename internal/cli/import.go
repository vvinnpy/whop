package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ttydeck/internal/nccm"
	"ttydeck/internal/secrets"
	"ttydeck/internal/sshconfig"
)

type importReport struct {
	Source             string   `json:"source"`
	Config             string   `json:"config"`
	Applied            bool     `json:"applied"`
	Added              []string `json:"added"`
	Skipped            []string `json:"skipped"`
	Warnings           []string `json:"warnings"`
	PasswordsAvailable int      `json:"passwords_available"`
	PasswordsStored    []string `json:"passwords_stored"`
	Backup             string   `json:"backup,omitempty"`
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
	imports, passwords, warnings := convertNCCMConnections(cfg.Connections, selected)
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
	keptAliases := make(map[string]bool, len(imports))
	for _, item := range imports {
		keptAliases[item.Alias] = true
	}
	keptPasswords := make(map[string]string)
	for alias, password := range passwords {
		if keptAliases[alias] {
			keptPasswords[alias] = password
		}
	}
	report := importReport{
		Source:             sourcePath,
		Config:             cfgPath,
		Applied:            *apply,
		Added:              []string{},
		Skipped:            append([]string{}, skipped...),
		Warnings:           append([]string{}, warnings...),
		PasswordsAvailable: len(keptPasswords),
		PasswordsStored:    []string{},
	}
	if *apply {
		var vault *secrets.Vault
		if len(keptPasswords) > 0 {
			path, err := vaultPath()
			if err != nil {
				fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
				return 1
			}
			_, statErr := os.Stat(path)
			master, err := readVaultMasterPassword(deps, errors.Is(statErr, os.ErrNotExist))
			if err != nil {
				fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
				return 1
			}
			vault, err = openOrCreateVault(path, master)
			if err != nil {
				fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
				return 1
			}
			defer vault.Close()
			for alias, password := range keptPasswords {
				if err := vault.Set(alias, password); err != nil {
					fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
					return 1
				}
			}
		}
		result, err := sshconfig.AddHosts(cfgPath, imports)
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		if vault != nil {
			if err := vault.Save(); err != nil {
				fmt.Fprintf(deps.Stderr, "ttydeck: save vault: %v\n", err)
				return 1
			}
			for alias := range keptPasswords {
				report.PasswordsStored = append(report.PasswordsStored, alias)
			}
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
	sort.Strings(report.PasswordsStored)
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
	if report.PasswordsAvailable > 0 {
		if *apply {
			fmt.Fprintf(deps.Stdout, "passwords stored in vault: %d\n", report.PasswordsAvailable)
		} else {
			fmt.Fprintf(deps.Stdout, "passwords available for encrypted vault: %d\n", report.PasswordsAvailable)
		}
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

func convertNCCMConnections(connections []nccm.Connection, selected map[string]bool) ([]sshconfig.HostImport, map[string]string, []string) {
	var imports []sshconfig.HostImport
	passwords := make(map[string]string)
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
		custom, err := nccm.ParseCustomArgs(connection.CustomArgs)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: customargs ignored: %v", name, err))
		}
		if custom.Port > 0 {
			port = custom.Port
		}
		identity := strings.TrimSpace(connection.Identity)
		if custom.Identity != "" {
			identity = custom.Identity
		}
		if custom.PasswordSet && custom.Password != "" && (custom.Mode == "expect" || custom.Mode == "expect-ssh-rsa") {
			passwords[alias] = custom.Password
			warnings = append(warnings, fmt.Sprintf("%s: password available for encrypted vault", name))
		} else if custom.PasswordSet {
			warnings = append(warnings, fmt.Sprintf("%s: -P password ignored because -z expect mode is not configured", name))
		}
		if len(custom.Unsupported) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: unsupported customargs ignored: %s", name, strings.Join(custom.Unsupported, ", ")))
		}
		imports = append(imports, sshconfig.HostImport{
			Alias:               alias,
			HostName:            address,
			User:                user,
			IdentityFile:        identity,
			Comment:             strings.TrimSpace(connection.Comment),
			Port:                port,
			ServerAliveInterval: connection.Keepalive,
			Compression:         custom.Compression,
			LegacySSHRSA:        custom.Mode == "ssh-rsa" || custom.Mode == "expect-ssh-rsa",
		})
	}
	return imports, passwords, warnings
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
