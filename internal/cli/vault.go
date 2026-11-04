package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/term"

	sshconn "whop/internal/connector/ssh"
	"whop/internal/secrets"
	"whop/internal/sshconfig"
)

const (
	vaultMasterPasswordEnv = "WHOP_VAULT_PASSWORD"
	vaultHostPasswordEnv   = "WHOP_HOST_PASSWORD"
)

type vaultStatus struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	AutoUnlock bool   `json:"auto_unlock"`
}

func runVault(args []string, deps Deps) int {
	if len(args) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop vault <init|set|list|delete|status> ...")
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
		fmt.Fprintf(deps.Stderr, "whop: unknown vault command %q\n", args[0])
		return 2
	}
}

func runVaultInit(args []string, deps Deps) int {
	fs := newFlagSet("whop vault init", deps)
	autoUnlock := fs.Bool("auto-unlock", false, "generate a 0600 local key file for non-interactive unlock")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop vault init")
		return 2
	}
	path, err := vaultPath()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(deps.Stderr, "whop: vault already exists: %s\n", path)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	var master string
	var keyPath string
	if *autoUnlock {
		keyPath, err = vaultKeyPath()
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		master, err = generateVaultKey(keyPath)
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
	} else {
		master, err = readVaultMasterPassword(deps, true)
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
	}
	vault, err := secrets.Create(path, master)
	if err != nil {
		if keyPath != "" {
			_ = os.Remove(keyPath)
		}
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	_ = vault.Close()
	fmt.Fprintf(deps.Stdout, "vault created: %s\n", path)
	if keyPath != "" {
		fmt.Fprintf(deps.Stdout, "auto-unlock key: %s\n", keyPath)
	}
	return 0
}

func runVaultSet(args []string, deps Deps) int {
	fs := newFlagSet("whop vault set", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop vault set <alias>")
		return 2
	}
	alias := strings.TrimSpace(fs.Arg(0))
	if alias == "" {
		fmt.Fprintln(deps.Stderr, "whop: alias is required")
		return 2
	}
	path, err := vaultPath()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	_, statErr := os.Stat(path)
	master, err := readVaultMasterPassword(deps, errors.Is(statErr, os.ErrNotExist))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	vault, err := openOrCreateVault(path, master)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer vault.Close()
	hostPassword, err := readHostPassword(deps, alias, true)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	if err := vault.Set(alias, hostPassword); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	if err := vault.Save(); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "password saved for %s\n", alias)
	return 0
}

func runVaultList(args []string, deps Deps) int {
	fs := newFlagSet("whop vault list", deps)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop vault list [flags]")
		return 2
	}
	vault, err := openExistingVault(deps)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer vault.Close()
	names, err := vault.List()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	sort.Strings(names)
	if *asJSON {
		encoded, err := json.MarshalIndent(names, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
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
	fs := newFlagSet("whop vault delete", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop vault delete <alias>")
		return 2
	}
	vault, err := openExistingVault(deps)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer vault.Close()
	if err := vault.Delete(fs.Arg(0)); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	if err := vault.Save(); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "password deleted for %s\n", fs.Arg(0))
	return 0
}

func runVaultStatus(args []string, deps Deps) int {
	fs := newFlagSet("whop vault status", deps)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop vault status [flags]")
		return 2
	}
	path, err := vaultPath()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	keyPath, _ := vaultKeyPath()
	_, keyErr := os.Stat(keyPath)
	status := vaultStatus{Path: path, AutoUnlock: keyErr == nil}
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		status.Exists = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	if *asJSON {
		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}
	if status.Exists {
		fmt.Fprintf(deps.Stdout, "vault exists: %s (auto-unlock=%v)\n", status.Path, status.AutoUnlock)
	} else {
		fmt.Fprintf(deps.Stdout, "vault not initialized: %s\n", status.Path)
	}
	return 0
}

func vaultPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("WHOP_VAULT_PATH")); value != "" {
		return value, nil
	}
	return secrets.DefaultPath()
}

func vaultKeyPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("WHOP_VAULT_KEY_FILE")); value != "" {
		return value, nil
	}
	path, err := vaultPath()
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(path)
	if ext == "" {
		return path + ".key", nil
	}
	return strings.TrimSuffix(path, ext) + ".key", nil
}

func generateVaultKey(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("auto-unlock key already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := base64.StdEncoding.EncodeToString(raw)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(file, key+"\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return key, nil
}

func readVaultKeyFile() (string, bool, error) {
	path, err := vaultKeyPath()
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", false, fmt.Errorf("empty auto-unlock key file: %s", path)
	}
	return key, true, nil
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
	if key, ok, err := readVaultKeyFile(); err != nil {
		return "", err
	} else if ok {
		return key, nil
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
