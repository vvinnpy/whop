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

	tea "github.com/charmbracelet/bubbletea"

	sshconn "ttydeck/internal/connector/ssh"
	"ttydeck/internal/sshconfig"
	"ttydeck/internal/ui"
)

// Version is the development version string.
const Version = "0.1.0-dev"

// Deps contains injectable process dependencies for tests.
type Deps struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	LoadConfig func(string) ([]sshconfig.Host, error)
	Connect    func(sshconfig.Host, io.Reader, io.Writer, io.Writer) error
	RunTUI     func(ui.Options) error
}

// NewDeps returns OS-backed dependencies.
func NewDeps() Deps {
	return Deps{
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		LoadConfig: sshconfig.Load,
		Connect:    defaultConnect,
		RunTUI:     defaultRunTUI,
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
	return deps
}

func runTUI(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	showAll := fs.Bool("all", false, "show Git/Auth hosts too")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	configPathValue := resolvePath(*configPath)
	initialFilter := strings.Join(fs.Args(), " ")
	err := deps.RunTUI(ui.Options{
		ConfigPath:    configPathValue,
		InitialFilter: initialFilter,
		ShowAll:       *showAll,
		Load: func() ([]sshconfig.Host, error) {
			return deps.LoadConfig(configPathValue)
		},
		Connect: func(host sshconfig.Host) tea.Cmd {
			return tea.Exec(&sshconn.ExecCommand{Runner: sshRunner(), Host: host}, func(err error) tea.Msg {
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

	hosts, err := deps.LoadConfig(resolvePath(*configPath))
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
	fmt.Fprintln(deps.Stdout, "NAME\tHOST\tUSER\tPORT\tKIND\tTAGS")
	for _, host := range hosts {
		fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\t%d\t%s\t%s\n",
			host.Alias, host.HostName, valueOrDash(host.User), host.Port, host.Kind, strings.Join(host.Tags, ","))
	}
	return 0
}

func runConnect(args []string, deps Deps) int {
	fs := newFlagSet("ttydeck connect", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
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

	hosts, err := deps.LoadConfig(resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	host, ok := sshconfig.Find(hosts, fs.Arg(0))
	if !ok {
		fmt.Fprintf(deps.Stderr, "ttydeck: host %q not found\n", fs.Arg(0))
		return 1
	}
	if err := deps.Connect(host, deps.Stdin, deps.Stdout, deps.Stderr); err != nil {
		fmt.Fprintf(deps.Stderr, "ttydeck: %v\n", err)
		return 1
	}
	return 0
}

func defaultConnect(host sshconfig.Host, stdin io.Reader, stdout, stderr io.Writer) error {
	runner := sshRunner()
	return runner.Run(context.Background(), host, stdin, stdout, stderr)
}

func sshRunner() *sshconn.Runner {
	connector := sshconn.New(sshconn.Options{})
	return sshconn.NewRunner(connector, nil)
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
  ttydeck version                   print version

Flags for ttydeck:
  -config path   path to ssh config (default ~/.ssh/config)
  -all           show Git/Auth hosts too

Flags for list:
  -config path   path to ssh config
  -all           show Git/Auth hosts too
  -json          emit JSON

Flags for connect:
  -config path   path to ssh config`)
}
