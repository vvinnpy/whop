package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"golang.org/x/term"

	sshconn "ttydeck/internal/connector/ssh"
	"ttydeck/internal/secrets"
	"ttydeck/internal/sshconfig"
)

const (
	vaultMasterPasswordEnv = "TTYDECK_VAULT_PASSWORD"
	vaultHostPasswordEnv   = "TTYDECK_HOST_PASSWORD"
)

type vaultStatus struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Entries int    `json:"entries,omitempty"`
}

func runVault(args []string, deps Deps) int {
	if len(args) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck vault <init|set|list|delete|status> ...")
		return 2
	}
	switch args[0] {
	case "init":
		return runVaultInit(args[1:], deps)
	case "set":
		return runVaultSet(args[1:], deps)
	case "list", "ls":
		return runVaultList(args[1:], deps)
	case "delete", "rm":
		return runVaultDelete(args[1:], deps)
	case "status":
		return runVaultStatus(args[1:], deps)
	default:
		fmt.Fprintf(deps.Stderr, "ttydeck: unknown vault command %q\n", args[0])
		return 2
	}
}

func runVaultInit(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck vault init", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck vault init")
		return 2
	}
	path, err := vaultPath()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: vault already exists: %s\n", path)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	master, err := readVaultMasterPassword(deps, true)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	vault, err := secrets.Create(path, master)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	_ = vault.Close()
	fmt.Fprintf(deps.Stdout, "vault created: %s\n", path)
	return 0
}

func runVaultSet(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck vault set", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck vault set <alias>")
		return 2
	}
	alias := strings.TrimSpace(fs.Arg(0))
	if alias == "" {
		fmt.Fprintln(deps.Stderr, "ttydeck: alias is required")
		return 2
	}
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
	vault, err := openOrCreateVault(path, master)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer vault.Close()
	hostPassword, err := readHostPassword(deps, alias, true)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if err := vault.Set(alias, hostPassword); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if err := vault.Save(); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "password saved for %s\n", alias)
	return 0
}

func runVaultList(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck vault list", deps)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck vault list [flags]")
		return 2
	}
	vault, err := openExistingVault(deps)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer vault.Close()
	names, err := vault.List()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	sort.Strings(names)
	if *asJSON {
		encoded, err := json.MarshalIndent(names, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	for _, name := range names {
		fmt.Fprintln(deps.Stdout, name)
	}
	return 0
}

func runVaultDelete(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck vault delete", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck vault delete <alias>")
		return 2
	}
	vault, err := openExistingVault(deps)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer vault.Close()
	if err := vault.Delete(fs.Arg(0)); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if err := vault.Save(); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "password deleted for %s\n", fs.Arg(0))
	return 0
}

func runVaultStatus(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck vault status", deps)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck vault status [flags]")
		return 2
	}
	path, err := vaultPath()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	status := vaultStatus{Path: path}
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		status.Exists = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if *asJSON {
		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	if status.Exists {
		fmt.Fprintf(deps.Stdout, "vault exists: %s\n", status.Path)
	} else {
		fmt.Fprintf(deps.Stdout, "vault not initialized: %s\n", status.Path)
	}
	return 0
}

func vaultPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("TTYDECK_VAULT_PATH")); value != "" {
		return value, nil
	}
	return secrets.DefaultPath()
}

func openExistingVault(deps Deps) (*secrets.Vault, error) {
	path, err := vaultPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("vault not initialized: %s", path)
		}
		return nil, err
	}
	master, err := readVaultMasterPassword(deps, false)
	if err != nil {
		return nil, err
	}
	return secrets.Open(path, master)
}

func openOrCreateVault(path, master string) (*secrets.Vault, error) {
	vault, err := secrets.Open(path, master)
	if err == nil {
		return vault, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return secrets.Create(path, master)
}

func readVaultMasterPassword(deps Deps, confirm bool) (string, error) {
	if value := os.Getenv(vaultMasterPasswordEnv); value != "" {
		return value, nil
	}
	first, err := readSecretLine(deps, "vault master password: ")
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", errors.New("vault master password is empty")
	}
	if confirm {
		second, err := readSecretLine(deps, "confirm vault master password: ")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", errors.New("vault master passwords do not match")
		}
	}
	return first, nil
}

func readHostPassword(deps Deps, alias string, confirm bool) (string, error) {
	if value := os.Getenv(vaultHostPasswordEnv); value != "" {
		return value, nil
	}
	first, err := readSecretLine(deps, fmt.Sprintf("%s password: ", alias))
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", errors.New("host password is empty")
	}
	if confirm {
		second, err := readSecretLine(deps, fmt.Sprintf("confirm %s password: ", alias))
		if err != nil {
			return "", err
		}
		if first != second {
			return "", errors.New("host passwords do not match")
		}
	}
	return first, nil
}

func readSecretLine(deps Deps, prompt string) (string, error) {
	if in, ok := deps.Stdin.(*os.File); ok && term.IsTerminal(int(in.Fd())) {
		fmt.Fprint(deps.Stderr, prompt)
		value, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(deps.Stderr)
		if err != nil {
			return "", err
		}
		return string(value), nil
	}
	fmt.Fprint(deps.Stderr, prompt)
	line, err := bufio.NewReader(deps.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// lazyVaultPasswordProvider opens the encrypted vault only when SSH actually
// attempts password authentication.
type lazyVaultPasswordProvider struct {
	mu        sync.Mutex
	path      string
	fallback  sshconn.PasswordProvider
	vault     *secrets.Vault
	openErr   error
	openTried bool
}

func (p *lazyVaultPasswordProvider) lookup(host sshconfig.Host) (string, bool, error) {
	if p == nil {
		return "", false, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.openTried {
		p.openTried = true
		if _, err := os.Stat(p.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", false, nil
			}
			p.openErr = err
			return "", false, err
		}
		master, err := readVaultMasterPassword(Deps{Stdin: os.Stdin, Stderr: os.Stderr}, false)
		if err != nil {
			p.openErr = err
			return "", false, err
		}
		p.vault, p.openErr = secrets.Open(p.path, master)
	}
	if p.openErr != nil {
		return "", false, p.openErr
	}
	if p.vault == nil {
		return "", false, nil
	}
	secret, found, err := p.vault.Get(host.Alias)
	return secret, found, err
}

func defaultPasswordProvider() sshconn.PasswordProvider {
	terminal := sshconn.TerminalPasswordProvider{In: os.Stdin, Out: os.Stderr}
	path, err := vaultPath()
	if err != nil {
		return terminal
	}
	store := &lazyVaultPasswordProvider{path: path, fallback: terminal}
	return &sshconn.StoredPasswordProvider{Lookup: store.lookup, Fallback: terminal}
}
