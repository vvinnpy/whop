// Package cli implements the ttydeck command-line interface.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	sshconn "ttydeck/internal/connector/ssh"
	"ttydeck/internal/core"
	"ttydeck/internal/session"
	"ttydeck/internal/sshconfig"
	"ttydeck/internal/store"
	"ttydeck/internal/ui"
)

// Version is the development version string.
const Version = "0.1.0-dev"

// Deps contains injectable process dependencies for tests.
type Deps struct {
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	LoadConfig   func(string) ([]sshconfig.Host, error)
	Connect      func(sshconfig.Host, []sshconfig.Host, io.Reader, io.Writer, io.Writer, bool) error
	RunTUI       func(ui.Options) error
	OpenStore    func() (MetaStore, error)
	RecordingDir string
}

// MetaStore is the metadata surface used by the CLI and TUI.
type MetaStore interface {
	All() (map[string]store.Meta, error)
	SetFavorite(string, bool) error
	SetNotes(string, string) error
	SetTags(string, []string) error
	RecordConnect(string, time.Time) error
	Close() error
}

// NewDeps returns OS-backed dependencies.
func NewDeps() Deps {
	return Deps{
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		LoadConfig:   sshconfig.Load,
		Connect:      defaultConnect,
		RunTUI:       defaultRunTUI,
		OpenStore:    defaultOpenStore,
		RecordingDir: defaultRecordingDir(),
	}
}

// Run executes the CLI and returns an exit code.
func Run(args []string, deps Deps) int {
	deps = normalizeDeps(deps)
	if len(args) == 0 {
		return runTUI(args, deps)
	}

	switch args[0] {
	case "list":
		return runList(args[1:], deps)
	case "connect":
		return runConnect(args[1:], deps)
	case "favorite":
		return runFavorite(args[1:], deps)
	case "note":
		return runNote(args[1:], deps)
	case "tag":
		return runTag(args[1:], deps)
	case "replay":
		return runReplay(args[1:], deps)
	case "search":
		return runSearch(args[1:], deps)
	case "version", "--version", "-v":
		fmt.Fprintf(deps.Stdout, "ttydeck %s\n", Version)
		return 0
	case "help", "--help", "-h":
		printUsage(deps.Stdout)
		return 0
	default:
		if strings.HasPrefix(args[0], "-") {
			return runTUI(args, deps)
		}
		// Bare arguments are an initial filter for the TUI, matching nccm.
		return runTUI(args, deps)
	}
}

func normalizeDeps(deps Deps) Deps {
	if deps.Stdin == nil {
		deps.Stdin = os.Stdin
	}
	if deps.Stdout == nil {
		deps.Stdout = os.Stdout
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}
	if deps.LoadConfig == nil {
		deps.LoadConfig = sshconfig.Load
	}
	if deps.Connect == nil {
		deps.Connect = defaultConnect
	}
	if deps.RunTUI == nil {
		deps.RunTUI = defaultRunTUI
	}
	if deps.OpenStore == nil {
		deps.OpenStore = defaultOpenStore
	}
	if deps.RecordingDir == "" {
		deps.RecordingDir = defaultRecordingDir()
	}
	return deps
}

func runTUI(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	showAll := fs.Bool("all", false, "show Git/Auth hosts too")
	record := fs.Bool("record", false, "record interactive sessions")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	configPathValue := resolvePath(*configPath)
	initialFilter := strings.Join(fs.Args(), " ")
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: open metadata: %v\n", err)
		return 1
	}
	defer metaStore.Close()

	err = deps.RunTUI(ui.Options{
		ConfigPath:    configPathValue,
		InitialFilter: initialFilter,
		ShowAll:       *showAll,
		Record:        *record,
		Load: func() ([]sshconfig.Host, error) {
			return loadHosts(deps, metaStore, configPathValue)
		},
		ToggleFavorite: func(host sshconfig.Host, favorite bool) error {
			return metaStore.SetFavorite(host.Alias, favorite)
		},
		Connect: func(host sshconfig.Host, record bool) tea.Cmd {
			return tea.Exec(&sshconn.ExecCommand{Runner: sshRunnerForConfig(configPathValue, record, deps.RecordingDir), Host: host}, func(err error) tea.Msg {
				if err == nil {
					_ = metaStore.RecordConnect(host.Alias, time.Now())
				}
				return ui.ConnectFinishedMsg{Err: err}
			})
		},
	})
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	return 0
}

func runList(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck list", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	showAll := fs.Bool("all", false, "show Git/Auth hosts too")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if !*showAll {
		hosts = sshconfig.Servers(hosts)
	}
	if hosts == nil {
		hosts = []sshconfig.Host{}
	}

	if *asJSON {
		encoded, err := json.MarshalIndent(hosts, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}

	if len(hosts) == 0 {
		fmt.Fprintln(deps.Stdout, "no hosts found")
		return 0
	}
	fmt.Fprintln(deps.Stdout, "NAME\tHOST\tUSER\tPORT\tKIND\tTAGS\tFAV\tLAST")
	for _, host := range hosts {
		favorite := "-"
		if host.Favorite {
			favorite = "*"
		}
		last := "-"
		if !host.LastConnectedAt.IsZero() {
			last = host.LastConnectedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			host.Alias, host.HostName, valueOrDash(host.User), host.Port, host.Kind, strings.Join(host.Tags, ","), favorite, last)
	}
	return 0
}

func runConnect(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck connect", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	record := fs.Bool("record", false, "record the session")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck connect [flags] <alias>")
		return 2
	}

	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	host, ok := sshconfig.Find(hosts, fs.Arg(0))
	if !ok {
		fmt.Fprintf(deps.Stderr, "ttydeck: host %q not found\n", fs.Arg(0))
		return 1
	}
	if err := deps.Connect(host, hosts, deps.Stdin, deps.Stdout, deps.Stderr, *record); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	if metaStore, err := deps.OpenStore(); err == nil {
		_ = metaStore.RecordConnect(host.Alias, time.Now())
		_ = metaStore.Close()
	}
	return 0
}

func loadHosts(deps Deps, metaStore MetaStore, configPath string) ([]sshconfig.Host, error) {
	hosts, err := deps.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	if metaStore == nil {
		metaStore, err = deps.OpenStore()
		if err != nil {
			return nil, fmt.Errorf("open metadata: %w", err)
		}
		defer metaStore.Close()
	}
	metas, err := metaStore.All()
	if err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	return core.ApplyMetadata(hosts, metas), nil
}

func runFavorite(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck favorite", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck favorite [flags] <alias> <on|off>")
		return 2
	}
	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	alias := fs.Arg(0)
	if _, ok := sshconfig.Find(hosts, alias); !ok {
		fmt.Fprintf(deps.Stderr, "ttydeck: host %q not found\n", alias)
		return 1
	}
	value := strings.ToLower(fs.Arg(1))
	favorite := value == "on" || value == "true" || value == "yes" || value == "1"
	if value != "on" && value != "off" && value != "true" && value != "false" && value != "yes" && value != "no" && value != "1" && value != "0" {
		fmt.Fprintln(deps.Stderr, "usage: favorite value must be on/off")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if err := metaStore.SetFavorite(alias, favorite); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "%s favorite=%v\n", alias, favorite)
	return 0
}

func runNote(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck note", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck note [flags] <alias> [text...]")
		return 2
	}
	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	alias := fs.Arg(0)
	host, ok := sshconfig.Find(hosts, alias)
	if !ok {
		fmt.Fprintf(deps.Stderr, "ttydeck: host %q not found\n", alias)
		return 1
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if fs.NArg() > 1 {
		if err := metaStore.SetNotes(alias, strings.Join(fs.Args()[1:], " ")); err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		fmt.Fprintf(deps.Stdout, "%s note updated\n", alias)
		return 0
	}
	fmt.Fprintln(deps.Stdout, host.Notes)
	return 0
}

func runTag(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck tag", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck tag [flags] <alias> [tag...]")
		return 2
	}
	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	alias := fs.Arg(0)
	host, ok := sshconfig.Find(hosts, alias)
	if !ok {
		fmt.Fprintf(deps.Stderr, "ttydeck: host %q not found\n", alias)
		return 1
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if fs.NArg() > 1 {
		if err := metaStore.SetTags(alias, fs.Args()[1:]); err != nil {
			fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
			return 1
		}
		fmt.Fprintf(deps.Stdout, "%s tags updated\n", alias)
		return 0
	}
	fmt.Fprintln(deps.Stdout, strings.Join(host.Tags, ","))
	return 0
}

func runReplay(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck replay", deps)
	speed := fs.Float64("speed", 1, "playback speed")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck replay [flags] <file.cast>")
		return 2
	}
	if err := session.ReplayFile(fs.Arg(0), deps.Stdout, *speed); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	return 0
}

func runSearch(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck search", deps)
	dir := fs.String("dir", deps.RecordingDir, "recording directory")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(deps.Stderr, "usage: ttydeck search [flags] <query>")
		return 2
	}
	matches, err := session.Search(*dir, strings.Join(fs.Args(), " "))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	for _, match := range matches {
		fmt.Fprintf(deps.Stdout, "%s:%d: %s\n", match.Path, match.Line, match.Text)
	}
	return 0
}

func defaultConnect(host sshconfig.Host, hosts []sshconfig.Host, stdin io.Reader, stdout, stderr io.Writer, record bool) error {
	runner := sshRunnerWithHosts(hosts, record, defaultRecordingDir())
	return runner.Run(context.Background(), host, stdin, stdout, stderr)
}

func sshRunnerForConfig(path string, record bool, recordingDir string) *sshconn.Runner {
	hosts, _ := sshconfig.Load(path)
	return sshRunnerWithHosts(hosts, record, recordingDir)
}

func sshRunnerWithHosts(hosts []sshconfig.Host, record bool, recordingDir string) *sshconn.Runner {
	byAlias := make(map[string]sshconfig.Host, len(hosts))
	for _, host := range hosts {
		byAlias[host.Alias] = host
	}
	connector := sshconn.New(sshconn.Options{
		Password: sshconn.TerminalPasswordProvider{In: os.Stdin, Out: os.Stderr},
		ResolveHost: func(alias string) (sshconfig.Host, bool) {
			host, ok := byAlias[alias]
			return host, ok
		},
	})
	runner := sshconn.NewRunner(connector, nil)
	if record {
		runner.RecordingFactory = func(host sshconfig.Host, width, height int) (io.WriteCloser, error) {
			recording, _, err := session.NewFileRecording(recordingDir, host.Alias, width, height, time.Now())
			return recording, err
		}
	}
	return runner
}

func defaultRunTUI(opts ui.Options) error {
	model := ui.New(opts)
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func newFlagSet(name string, deps Deps) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(deps.Stderr)
	return fs
}

func defaultOpenStore() (MetaStore, error) {
	return store.Open(defaultStorePath())
}

func defaultStorePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "ttydeck", "state.db")
}

func defaultRecordingDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "ttydeck", "recordings")
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("~", ".ssh", "config")
	}
	return filepath.Join(home, ".ssh", "config")
}

func resolvePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `ttydeck - fast, local-first terminal connection deck

Usage:
  ttydeck [flags] [filter...]       open the interactive host list
  ttydeck list [flags]              list hosts
  ttydeck connect [flags] <alias>   connect to a host
  ttydeck favorite [flags] <alias> <on|off>
  ttydeck note [flags] <alias> [text...]
  ttydeck tag [flags] <alias> [tag...]
  ttydeck replay [flags] <file.cast>
  ttydeck search [flags] <query>
  ttydeck version                   print version

Flags for ttydeck:
  -config path   path to ssh config (default ~/.ssh/config)
  -all           show Git/Auth hosts too
  -record        record interactive sessions

Flags for list:
  -config path   path to ssh config
  -all           show Git/Auth hosts too
  -json          emit JSON

Flags for connect:
  -config path   path to ssh config
  -record        record the session`)
}
