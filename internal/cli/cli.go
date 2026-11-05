// Package cli implements the whop command-line interface.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"whop/internal/automation"
	coreconnector "whop/internal/connector"
	sshconn "whop/internal/connector/ssh"
	"whop/internal/core"
	"whop/internal/session"
	"whop/internal/sshconfig"
	"whop/internal/store"
	"whop/internal/ui"
	"whop/internal/zmodem"
)

const (
	tuiNoticeEnv      = "WHOP_TUI_NOTICE"
	tuiNoticeLevelEnv = "WHOP_TUI_NOTICE_LEVEL"
	tuiNoticeScopeEnv = "WHOP_TUI_NOTICE_SCOPE"
	tuiMacroPauseEnv  = "WHOP_TUI_MACRO_PAUSE"
)

// Version is overridden at build time with -ldflags. When empty, versionString
// falls back to Go build metadata so local and installed builds stay accurate.
var Version string

// PreparedConnect is an authenticated interactive session waiting for the
// local terminal. Run blocks until the remote shell exits.
type PreparedConnect interface {
	Run(context.Context, io.Reader, io.Writer, io.Writer) error
	Close() error
}

// Deps contains injectable process dependencies for tests.
type Deps struct {
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	LoadConfig     func(string) ([]sshconfig.Host, error)
	Connect        func(sshconfig.Host, []sshconfig.Host, io.Reader, io.Writer, io.Writer, bool, bool) error
	PrepareConnect func(context.Context, sshconfig.Host, []sshconfig.Host, bool, bool) (PreparedConnect, error)
	RunTUI         func(ui.Options) error
	OpenStore      func() (MetaStore, error)
	RecordingDir   string
	ZmodemPut      func(sshconfig.Host, []sshconfig.Host, []string, bool, bool, io.Writer) error
	ZmodemGet      func(sshconfig.Host, []sshconfig.Host, []string, string, int64, bool, bool, io.Writer) error
	RunCommand     func(sshconfig.Host, []sshconfig.Host, string, bool) (string, error)
	RunMacro       func(context.Context, sshconfig.Host, []sshconfig.Host, automation.Macro, io.Writer, io.Writer, automation.EventFunc, bool) error
	Forward        func(context.Context, sshconfig.Host, []sshconfig.Host, coreconnector.ForwardSpec, bool, func(coreconnector.ForwardStatus) error) error
	RunSFTP        func(sshconfig.Host, []sshconfig.Host, bool) error
	Diagnose       func(sshconfig.Host, []sshconfig.Host, bool) error
	Password       sshconn.PasswordProvider
	Relaunch       func() error
	LookupEnv      func(string) (string, bool)
	UseAgent       bool
}

// MetaStore is the metadata surface used by the CLI and TUI.
type MetaStore interface {
	All() (map[string]store.Meta, error)
	SetFavorite(string, bool) error
	SetLoginOverride(string, *bool) error
	SetRecord(string, bool) error
	SetNotes(string, string) error
	SetTags(string, []string) error
	RenameHost(string, string) error
	RecordConnect(string, time.Time) error
	SetSnippet(string, string) error
	Snippets() (map[string]string, error)
	DeleteSnippet(string) error
	IndexTranscript(store.Transcript) error
	ReconcileTranscriptRoot(string, []string) (int, error)
	SearchTranscripts(string) ([]store.TranscriptMatch, error)
	SetMacroDefinition(string, []byte) error
	MacroDefinition(string) ([]byte, bool, error)
	MacroDefinitions() (map[string][]byte, error)
	DeleteMacroDefinition(string) error
	AppendAudit(store.AuditEvent) error
	AuditEvents(int) ([]store.AuditEvent, error)
	ClearAudit() error
	Close() error
}

// NewDeps returns OS-backed dependencies.
func NewDeps() Deps {
	password := defaultPasswordProvider()
	return Deps{
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		LoadConfig: sshconfig.Load,
		Connect: func(host sshconfig.Host, hosts []sshconfig.Host, stdin io.Reader, stdout, stderr io.Writer, record, useAgent bool) error {
			return defaultConnect(host, hosts, stdin, stdout, stderr, record, useAgent, password)
		},
		PrepareConnect: func(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, record, useAgent bool) (PreparedConnect, error) {
			return defaultPrepareConnect(ctx, host, hosts, record, useAgent, password)
		},
		RunTUI:       defaultRunTUI,
		OpenStore:    defaultOpenStore,
		RecordingDir: defaultRecordingDir(),
		ZmodemPut: func(host sshconfig.Host, hosts []sshconfig.Host, files []string, overwrite, useAgent bool, out io.Writer) error {
			return defaultZmodemPut(host, hosts, files, overwrite, useAgent, out, password)
		},
		ZmodemGet: func(host sshconfig.Host, hosts []sshconfig.Host, remoteFiles []string, dest string, maxSize int64, overwrite, useAgent bool, out io.Writer) error {
			return defaultZmodemGet(host, hosts, remoteFiles, dest, maxSize, overwrite, useAgent, out, password)
		},
		RunCommand: func(host sshconfig.Host, hosts []sshconfig.Host, command string, useAgent bool) (string, error) {
			return defaultRunCommand(host, hosts, command, useAgent, password)
		},
		RunMacro: func(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, macro automation.Macro, stdout, stderr io.Writer, onEvent automation.EventFunc, useAgent bool) error {
			return defaultRunMacro(ctx, host, hosts, macro, stdout, stderr, onEvent, useAgent, password)
		},
		Forward: func(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, spec coreconnector.ForwardSpec, useAgent bool, ready func(coreconnector.ForwardStatus) error) error {
			return defaultForward(ctx, host, hosts, spec, useAgent, ready, password)
		},
		RunSFTP: func(host sshconfig.Host, hosts []sshconfig.Host, useAgent bool) error {
			return defaultRunSFTP(host, hosts, useAgent, password)
		},
		Diagnose: func(host sshconfig.Host, hosts []sshconfig.Host, useAgent bool) error {
			return defaultDiagnose(host, hosts, useAgent, password)
		},
		Password:  password,
		Relaunch:  relaunchProcess,
		LookupEnv: os.LookupEnv,
	}
}

// Run executes the CLI and returns an exit code.
func Run(args []string, deps Deps) int {
	args, useAgent := extractAgentFlag(args)
	deps = normalizeDeps(deps)
	deps.UseAgent = useAgent
	if len(args) == 0 {
		return runTUI(args, deps)
	}
	if path, ok := helpRequestPath(args); ok {
		if len(path) == 0 {
			printUsage(deps.Stdout)
		} else {
			return runHelp(path, deps)
		}
		return 0
	}

	switch args[0] {
	case "list":
		return runList(args[1:], deps)
	case "connect":
		return runConnect(args[1:], deps)
	case "forward":
		return runForward(args[1:], deps)
	case "sftp":
		return runSFTP(args[1:], deps)
	case "favorite":
		return runFavorite(args[1:], deps)
	case "record":
		return runRecord(args[1:], deps)
	case "access":
		return runAccess(args[1:], deps)
	case "note":
		return runNote(args[1:], deps)
	case "tag":
		return runTag(args[1:], deps)
	case "replay":
		return runReplay(args[1:], deps)
	case "search":
		return runSearch(args[1:], deps)
	case "index":
		return runIndex(args[1:], deps)
	case "zmodem":
		return runZmodem(args[1:], deps)
	case "run":
		return runRemoteCommand(args[1:], deps)
	case "snippet":
		return runSnippet(args[1:], deps)
	case "macro":
		return runMacro(args[1:], deps)
	case "audit":
		return runAudit(args[1:], deps)
	case "import":
		return runImport(args[1:], deps)
	case "vault":
		return runVault(args[1:], deps)
	case "diagnose":
		return runDiagnose(args[1:], deps)
	case "version", "--version", "-v":
		fmt.Fprintf(deps.Stdout, "whop %s\n", versionString())
		return 0
	case "help", "--help", "-h":
		return runHelp(args[1:], deps)
	default:
		if strings.HasPrefix(args[0], "-") {
			return runTUI(args, deps)
		}
		// Bare arguments are an initial filter for the TUI, matching nccm.
		return runTUI(args, deps)
	}
}

func extractAgentFlag(args []string) ([]string, bool) {
	useAgent := envBool("WHOP_SSH_AGENT")
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "-agent", "--agent":
			useAgent = true
		case "-no-agent", "--no-agent":
			useAgent = false
		default:
			filtered = append(filtered, arg)
		}
	}
	return filtered, useAgent
}

func envBool(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
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
	if deps.Password == nil {
		deps.Password = defaultPasswordProvider()
	}
	if deps.Connect == nil {
		deps.Connect = func(host sshconfig.Host, hosts []sshconfig.Host, stdin io.Reader, stdout, stderr io.Writer, record, useAgent bool) error {
			return defaultConnect(host, hosts, stdin, stdout, stderr, record, useAgent, deps.Password)
		}
	}
	if deps.PrepareConnect == nil {
		deps.PrepareConnect = func(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, record, useAgent bool) (PreparedConnect, error) {
			return defaultPrepareConnect(ctx, host, hosts, record, useAgent, deps.Password)
		}
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
	if deps.ZmodemPut == nil {
		deps.ZmodemPut = func(host sshconfig.Host, hosts []sshconfig.Host, files []string, overwrite, useAgent bool, out io.Writer) error {
			return defaultZmodemPut(host, hosts, files, overwrite, useAgent, out, deps.Password)
		}
	}
	if deps.ZmodemGet == nil {
		deps.ZmodemGet = func(host sshconfig.Host, hosts []sshconfig.Host, remoteFiles []string, dest string, maxSize int64, overwrite, useAgent bool, out io.Writer) error {
			return defaultZmodemGet(host, hosts, remoteFiles, dest, maxSize, overwrite, useAgent, out, deps.Password)
		}
	}
	if deps.RunCommand == nil {
		deps.RunCommand = func(host sshconfig.Host, hosts []sshconfig.Host, command string, useAgent bool) (string, error) {
			return defaultRunCommand(host, hosts, command, useAgent, deps.Password)
		}
	}
	if deps.Diagnose == nil {
		deps.Diagnose = func(host sshconfig.Host, hosts []sshconfig.Host, useAgent bool) error {
			return defaultDiagnose(host, hosts, useAgent, deps.Password)
		}
	}
	if deps.RunMacro == nil {
		deps.RunMacro = func(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, macro automation.Macro, stdout, stderr io.Writer, onEvent automation.EventFunc, useAgent bool) error {
			return defaultRunMacro(ctx, host, hosts, macro, stdout, stderr, onEvent, useAgent, deps.Password)
		}
	}
	if deps.Forward == nil {
		deps.Forward = func(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, spec coreconnector.ForwardSpec, useAgent bool, ready func(coreconnector.ForwardStatus) error) error {
			return defaultForward(ctx, host, hosts, spec, useAgent, ready, deps.Password)
		}
	}
	if deps.RunSFTP == nil {
		deps.RunSFTP = func(host sshconfig.Host, hosts []sshconfig.Host, useAgent bool) error {
			return defaultRunSFTP(host, hosts, useAgent, deps.Password)
		}
	}
	if deps.LookupEnv == nil {
		deps.LookupEnv = os.LookupEnv
	}
	if deps.Relaunch == nil {
		deps.Relaunch = relaunchProcess
	}
	return deps
}

func runTUI(args []string, deps Deps) int {
	fs := newFlagSet("whop", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	showAll := fs.Bool("all", false, "show service endpoints too")
	record := fs.Bool("record", false, "record interactive sessions")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	tuiNotice := ui.Notice{
		Text:  os.Getenv(tuiNoticeEnv),
		Level: ui.StatusResult,
		Scope: os.Getenv(tuiNoticeScopeEnv),
	}
	if os.Getenv(tuiNoticeLevelEnv) == "error" {
		tuiNotice.Level = ui.StatusError
	}
	_ = os.Unsetenv(tuiNoticeEnv)
	_ = os.Unsetenv(tuiNoticeLevelEnv)
	_ = os.Unsetenv(tuiNoticeScopeEnv)
	pauseAfterMacro := envBool(tuiMacroPauseEnv)
	_ = os.Unsetenv(tuiMacroPauseEnv)
	if pauseAfterMacro {
		waitForMacroResult(deps, tuiNotice.Text)
	}

	configPathValue := resolvePath(*configPath)
	initialFilter := strings.Join(fs.Args(), " ")
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: open metadata: %v\n", err)
		return 1
	}
	defer metaStore.Close()

	for {
		var pendingConnect *connectRequest
		var pendingForward *forwardRequest
		var pendingSFTP *sftpRequest
		var pendingMacro *macroRequest
		options := ui.Options{
			ConfigPath:    configPathValue,
			InitialFilter: initialFilter,
			InitialNotice: tuiNotice,
			ShowAll:       *showAll,
			Record:        *record,
			Load: func() ([]sshconfig.Host, error) {
				return loadHosts(deps, metaStore, configPathValue)
			},
			ToggleFavorite: func(host sshconfig.Host, favorite bool) error {
				return metaStore.SetFavorite(host.Alias, favorite)
			},
			PreviewEdit: func(host sshconfig.Host, edit sshconfig.Edit) (sshconfig.Preview, error) {
				return sshconfig.PreviewHostChange(configPathValue, host.Alias, edit)
			},
			EditHost: func(host sshconfig.Host, edit sshconfig.Edit) error {
				return sshconfig.EditHost(configPathValue, host.Alias, edit)
			},
			PreviewAdd: func(imp sshconfig.HostImport) (sshconfig.Preview, error) {
				return sshconfig.PreviewHostAdd(configPathValue, imp)
			},
			AddHost: func(imp sshconfig.HostImport, expectedHash string) error {
				return sshconfig.AddHost(configPathValue, imp, expectedHash)
			},
			RenameHost: func(oldAlias, newAlias string) error {
				if err := metaStore.RenameHost(oldAlias, newAlias); err != nil {
					return err
				}
				return renameVaultPassword(deps, oldAlias, newAlias)
			},
			SetHostPassword: func(host sshconfig.Host, password string) error {
				return saveHostPassword(deps, host.Alias, password)
			},
			SetTags: func(host sshconfig.Host, tags []string) error {
				return metaStore.SetTags(host.Alias, tags)
			},
			SaveNotes: func(host sshconfig.Host, note string) error {
				return metaStore.SetNotes(host.Alias, note)
			},
			SetRecord: func(host sshconfig.Host, enabled bool) error {
				return metaStore.SetRecord(host.Alias, enabled)
			},
			SetLoginMode: func(host sshconfig.Host, mode sshconfig.AccessMode) error {
				switch mode {
				case sshconfig.AccessLogin:
					value := true
					return metaStore.SetLoginOverride(host.Alias, &value)
				case sshconfig.AccessService:
					value := false
					return metaStore.SetLoginOverride(host.Alias, &value)
				default:
					return metaStore.SetLoginOverride(host.Alias, nil)
				}
			},
			Connect: func(host sshconfig.Host, record bool) tea.Cmd {
				return func() tea.Msg {
					hosts, err := loadHosts(deps, metaStore, configPathValue)
					if err != nil {
						return ui.ConnectAttemptFinishedMsg{Host: host.Alias, Err: err}
					}
					prepared, err := deps.PrepareConnect(context.Background(), host, hosts, record, deps.UseAgent)
					if errors.Is(err, sshconn.ErrInteractiveAuthRequired) {
						// Passwords, OTPs, and an encrypted vault need terminal input.
						// Restore the terminal and use the normal interactive flow.
						return ui.ConnectAttemptFinishedMsg{
							Host: host.Alias,
							Handover: func() {
								pendingConnect = &connectRequest{host: host, record: record, hosts: hosts}
							},
						}
					}
					if err != nil {
						return ui.ConnectAttemptFinishedMsg{Host: host.Alias, Err: err}
					}
					return ui.ConnectAttemptFinishedMsg{
						Host: host.Alias,
						Handover: func() {
							pendingConnect = &connectRequest{host: host, record: record, prepared: prepared}
						},
					}
				}
			},
			Forward: func(host sshconfig.Host, spec coreconnector.ForwardSpec) tea.Cmd {
				pendingForward = &forwardRequest{host: host, spec: spec}
				return tea.Quit
			},
			SFTP: func(host sshconfig.Host) tea.Cmd {
				pendingSFTP = &sftpRequest{host: host}
				return tea.Quit
			},
			LoadMacros: func() ([]string, error) {
				return loadMacroNames(metaStore)
			},
			RunMacro: func(host sshconfig.Host, name string) tea.Cmd {
				pendingMacro = &macroRequest{host: host, name: name}
				return tea.Quit
			},
		}
		if err := deps.RunTUI(options); err != nil {
			if pendingConnect != nil && pendingConnect.prepared != nil {
				_ = pendingConnect.prepared.Close()
			}
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}

		switch {
		case pendingConnect != nil:
			request := *pendingConnect
			pendingConnect = nil
			// The TUI has restored the terminal. Key-based sessions are already
			// authenticated with their shell ready; interactive-auth hosts fall
			// back to the normal terminal prompt flow here.
			var err error
			if request.prepared != nil {
				err = request.prepared.Run(context.Background(), deps.Stdin, deps.Stdout, deps.Stderr)
				_ = request.prepared.Close()
			} else {
				err = deps.Connect(request.host, request.hosts, deps.Stdin, deps.Stdout, deps.Stderr, request.record, deps.UseAgent)
			}
			if err != nil {
				tuiNotice = ui.Notice{
					Text:  fmt.Sprintf("connection failed · %s: %v", request.host.Alias, err),
					Level: ui.StatusError,
					Scope: request.host.Alias,
				}
			} else {
				_ = metaStore.RecordConnect(request.host.Alias, time.Now())
				tuiNotice = ui.Notice{
					Text:  "session ended · " + request.host.Alias,
					Level: ui.StatusResult,
					Scope: request.host.Alias,
				}
			}
			return relaunchTUI(deps, tuiNotice)

		case pendingForward != nil:
			request := *pendingForward
			pendingForward = nil
			hosts, loadErr := loadHosts(deps, metaStore, configPathValue)
			if loadErr != nil {
				tuiNotice = ui.Notice{
					Text:  fmt.Sprintf("forwarding failed · %s: %v", request.host.Alias, loadErr),
					Level: ui.StatusError,
					Scope: request.host.Alias,
				}
				return relaunchTUI(deps, tuiNotice)
			}
			if err := runForwardInteractive(deps, request.host, hosts, request.spec); err != nil {
				tuiNotice = ui.Notice{
					Text:  fmt.Sprintf("forwarding failed · %s: %v", request.host.Alias, err),
					Level: ui.StatusError,
					Scope: request.host.Alias,
				}
			} else {
				tuiNotice = ui.Notice{
					Text:  "forwarding ended · " + request.host.Alias,
					Level: ui.StatusResult,
					Scope: request.host.Alias,
				}
			}
			return relaunchTUI(deps, tuiNotice)

		case pendingSFTP != nil:
			request := *pendingSFTP
			pendingSFTP = nil
			hosts, loadErr := loadHosts(deps, metaStore, configPathValue)
			if loadErr != nil {
				tuiNotice = ui.Notice{
					Text:  fmt.Sprintf("SFTP failed · %s: %v", request.host.Alias, loadErr),
					Level: ui.StatusError,
					Scope: request.host.Alias,
				}
				return relaunchTUI(deps, tuiNotice)
			}
			if err := deps.RunSFTP(request.host, hosts, deps.UseAgent); err != nil {
				tuiNotice = ui.Notice{
					Text:  fmt.Sprintf("SFTP failed · %s: %v", request.host.Alias, err),
					Level: ui.StatusError,
					Scope: request.host.Alias,
				}
			} else {
				tuiNotice = ui.Notice{
					Text:  "SFTP closed · " + request.host.Alias,
					Level: ui.StatusResult,
					Scope: request.host.Alias,
				}
			}
			return relaunchTUI(deps, tuiNotice)

		case pendingMacro != nil:
			request := *pendingMacro
			pendingMacro = nil
			text, level := runTUIMacro(deps, metaStore, configPathValue, request.host, request.name)
			tuiNotice = ui.Notice{Text: text, Level: level, Scope: request.host.Alias}
			// The next process shows the macro result before reopening the TUI.
			// Waiting here would race with Bubble Tea's old stdin reader.
			_ = os.Setenv(tuiMacroPauseEnv, "1")
			return relaunchTUI(deps, tuiNotice)

		default:
			return 0
		}
	}
}

func relaunchTUI(deps Deps, notice ui.Notice) int {
	if notice.Text != "" {
		_ = os.Setenv(tuiNoticeEnv, notice.Text)
		level := "result"
		if notice.Level == ui.StatusError {
			level = "error"
		}
		_ = os.Setenv(tuiNoticeLevelEnv, level)
		if notice.Scope != "" {
			_ = os.Setenv(tuiNoticeScopeEnv, notice.Scope)
		} else {
			_ = os.Unsetenv(tuiNoticeScopeEnv)
		}
	} else {
		_ = os.Unsetenv(tuiNoticeEnv)
		_ = os.Unsetenv(tuiNoticeLevelEnv)
		_ = os.Unsetenv(tuiNoticeScopeEnv)
	}
	if err := deps.Relaunch(); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: restart TUI: %v\n", err)
		return 1
	}
	return 0
}

func waitForMacroResult(deps Deps, notice string) {
	fmt.Fprintln(deps.Stderr)
	if notice != "" {
		fmt.Fprintln(deps.Stderr, notice)
	}
	fmt.Fprint(deps.Stderr, "Press Enter to return to the host list...")
	if deps.Stdin != nil {
		_, _ = bufio.NewReader(deps.Stdin).ReadByte()
	}
	fmt.Fprintln(deps.Stderr)
}

type connectRequest struct {
	host     sshconfig.Host
	record   bool
	hosts    []sshconfig.Host
	prepared PreparedConnect
}

type macroRequest struct {
	host sshconfig.Host
	name string
}

func runTUIMacro(deps Deps, metaStore MetaStore, configPath string, host sshconfig.Host, name string) (string, ui.StatusLevel) {
	macro, err := loadStoredMacro(metaStore, name, deps.LookupEnv)
	if err != nil {
		return fmt.Sprintf("macro %s failed · %s: %v", name, host.Alias, err), ui.StatusError
	}
	hosts, err := loadHosts(deps, metaStore, configPath)
	if err != nil {
		return fmt.Sprintf("macro %s failed · %s: %v", name, host.Alias, err), ui.StatusError
	}
	collector := newAuditCollector(metaStore)
	macroAuditStarted(collector, host.Alias, name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err = deps.RunMacro(ctx, host, hosts, macro, deps.Stdout, deps.Stderr, collector.automationEvent(host.Alias, name), deps.UseAgent)
	if err != nil {
		macroAuditFailed(collector, host.Alias, name, err)
	} else {
		_ = metaStore.RecordConnect(host.Alias, time.Now())
	}
	_ = macroAuditFlush(collector, deps.Stderr)
	if err != nil {
		return fmt.Sprintf("macro %s failed · %s: %v", name, host.Alias, err), ui.StatusError
	}
	return fmt.Sprintf("macro %s completed · %s", name, host.Alias), ui.StatusResult
}

func runList(args []string, deps Deps) int {
	fs := newFlagSet("whop list", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	showAll := fs.Bool("all", false, "show service endpoints too")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	if !*showAll {
		hosts = sshconfig.Servers(hosts)
	}
	if hosts == nil {
		hosts = []sshconfig.Host{}
	}

	if *asJSON {
		type listedHost struct {
			sshconfig.Host
			Loginable bool `json:"loginable"`
		}
		listed := make([]listedHost, 0, len(hosts))
		for _, host := range hosts {
			listed = append(listed, listedHost{Host: host, Loginable: host.IsLoginable()})
		}
		encoded, err := json.MarshalIndent(listed, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return 0
	}

	if len(hosts) == 0 {
		fmt.Fprintln(deps.Stdout, "no hosts found")
		return 0
	}
	fmt.Fprintln(deps.Stdout, "NAME\tHOST\tUSER\tPORT\tKIND\tACCESS\tTAGS\tFAV\tLAST")
	for _, host := range hosts {
		favorite := "-"
		if host.Favorite {
			favorite = "*"
		}
		last := "-"
		if !host.LastConnectedAt.IsZero() {
			last = host.LastConnectedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			host.Alias, host.HostName, valueOrDash(host.User), host.Port, host.Kind, host.EffectiveAccess(), strings.Join(host.Tags, ","), favorite, last)
	}
	return 0
}

func runConnect(args []string, deps Deps) int {
	fs := newFlagSet("whop connect", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	record := fs.Bool("record", false, "record the session")
	asJSON := fs.Bool("json", false, "output structured error diagnostics")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop connect [flags] <alias>")
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
	stdout, stderr := deps.Stdout, deps.Stderr
	if *asJSON {
		stdout, stderr = io.Discard, io.Discard
	}
	if err := deps.Connect(host, hosts, deps.Stdin, stdout, stderr, *record || host.Record, deps.UseAgent); err != nil {
		if *asJSON {
			writeDiagnostic(deps, host.Alias, err, true)
			return 1
		}
		if errors.Is(err, sshconn.ErrInteractiveUnavailable) {
			fmt.Fprintf(deps.Stderr, "whop: %s did not provide an interactive shell\n", host.Alias)
			fmt.Fprintf(deps.Stderr, "try: whop run %s help\n", host.Alias)
			fmt.Fprintf(deps.Stderr, "or mark it as a service: whop access %s service\n", host.Alias)
			return 0
		}
		writeDiagnostic(deps, host.Alias, err, false)
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
	fs := newFlagSet("whop favorite", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop favorite [flags] <alias> <on|off>")
		return 2
	}
	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	alias := fs.Arg(0)
	if _, ok := sshconfig.Find(hosts, alias); !ok {
		fmt.Fprintf(deps.Stderr, "whop: host %q not found\n", alias)
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
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if err := metaStore.SetFavorite(alias, favorite); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "%s favorite=%v\n", alias, favorite)
	return 0
}

func runAccess(args []string, deps Deps) int {
	fs := newFlagSet("whop access", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop access [flags] <alias> [auto|login|service]")
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
	if fs.NArg() == 1 {
		fmt.Fprintf(deps.Stdout, "%s access=%s effective=%s\n", host.Alias, host.AccessMode, host.EffectiveAccess())
		return 0
	}

	mode, ok := sshconfig.ParseAccessMode(fs.Arg(1))
	if !ok {
		fmt.Fprintln(deps.Stderr, "usage: access must be auto, login, or service")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	var override *bool
	switch mode {
	case sshconfig.AccessLogin:
		value := true
		override = &value
	case sshconfig.AccessService:
		value := false
		override = &value
	}
	if err := metaStore.SetLoginOverride(host.Alias, override); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	host.AccessMode = mode
	fmt.Fprintf(deps.Stdout, "%s access=%s effective=%s\n", host.Alias, host.AccessMode, host.EffectiveAccess())
	return 0
}

func runNote(args []string, deps Deps) int {
	fs := newFlagSet("whop note", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop note [flags] <alias> [text...]")
		return 2
	}
	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	alias := fs.Arg(0)
	host, ok := sshconfig.Find(hosts, alias)
	if !ok {
		fmt.Fprintf(deps.Stderr, "whop: host %q not found\n", alias)
		return 1
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if fs.NArg() > 1 {
		if err := metaStore.SetNotes(alias, strings.Join(fs.Args()[1:], " ")); err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintf(deps.Stdout, "%s note updated\n", alias)
		return 0
	}
	fmt.Fprintln(deps.Stdout, host.Notes)
	return 0
}

func runTag(args []string, deps Deps) int {
	fs := newFlagSet("whop tag", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop tag [flags] <alias> [tag...]")
		return 2
	}
	hosts, err := loadHosts(deps, nil, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	alias := fs.Arg(0)
	host, ok := sshconfig.Find(hosts, alias)
	if !ok {
		fmt.Fprintf(deps.Stderr, "whop: host %q not found\n", alias)
		return 1
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if fs.NArg() > 1 {
		if err := metaStore.SetTags(alias, fs.Args()[1:]); err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintf(deps.Stdout, "%s tags updated\n", alias)
		return 0
	}
	fmt.Fprintln(deps.Stdout, strings.Join(host.Tags, ","))
	return 0
}

func runReplay(args []string, deps Deps) int {
	fs := newFlagSet("whop replay", deps)
	speed := fs.Float64("speed", 1, "playback speed")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop replay [flags] <file.cast>")
		return 2
	}
	if err := session.ReplayFile(fs.Arg(0), deps.Stdout, *speed); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	return 0
}

func runSearch(args []string, deps Deps) int {
	fs := newFlagSet("whop search", deps)
	dir := fs.String("dir", deps.RecordingDir, "recording directory")
	files := fs.Bool("files", false, "scan transcript files instead of the SQLite index")
	noSync := fs.Bool("no-sync", false, "search the existing index without scanning recordings")
	skipInvalid := fs.Bool("skip-invalid", false, "skip malformed recordings while syncing the index")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop search [flags] <query>")
		return 2
	}
	query := strings.Join(fs.Args(), " ")
	if *files {
		matches, err := session.Search(*dir, query)
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		if *asJSON {
			if matches == nil {
				matches = []session.Match{}
			}
			encoded, err := json.MarshalIndent(matches, "", "  ")
			if err != nil {
				fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
				return 1
			}
			fmt.Fprintln(deps.Stdout, string(encoded))
			return 0
		}
		for _, match := range matches {
			fmt.Fprintf(deps.Stdout, "%s:%d: %s\n", match.Path, match.Line, match.Text)
		}
		return 0
	}

	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if !*noSync {
		if _, _, err := syncTranscriptIndex(metaStore, *dir, *skipInvalid, false); err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
	}
	matches, err := metaStore.SearchTranscripts(query)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	return writeTranscriptMatches(deps, matches, *asJSON)
}

func runRemoteCommand(args []string, deps Deps) int {
	fs := newFlagSet("whop run", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop run [flags] <alias> <command...>")
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
	command := strings.Join(fs.Args()[1:], " ")
	output, err := deps.RunCommand(host, hosts, command, deps.UseAgent)
	if output != "" {
		fmt.Fprint(deps.Stdout, output)
		if !strings.HasSuffix(output, "\n") {
			fmt.Fprintln(deps.Stdout)
		}
	}
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	return 0
}

func runSnippet(args []string, deps Deps) int {
	if len(args) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop snippet <set|list|delete|run> ...")
		return 2
	}
	switch args[0] {
	case "set":
		return runSnippetSet(args[1:], deps)
	case "list", "ls":
		return runSnippetList(args[1:], deps)
	case "delete", "rm":
		return runSnippetDelete(args[1:], deps)
	case "run":
		return runSnippetRun(args[1:], deps)
	default:
		fmt.Fprintf(deps.Stderr, "whop: unknown snippet command %q\n", args[0])
		return 2
	}
}

func runSnippetSet(args []string, deps Deps) int {
	fs := newFlagSet("whop snippet set", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop snippet set <name> <command...>")
		return 2
	}
	name := fs.Arg(0)
	command := strings.Join(fs.Args()[1:], " ")
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if err := metaStore.SetSnippet(name, command); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "snippet %s saved\n", name)
	return 0
}

func runSnippetList(args []string, deps Deps) int {
	fs := newFlagSet("whop snippet list", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	snippets, err := metaStore.Snippets()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	names := make([]string, 0, len(snippets))
	for name := range snippets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(deps.Stdout, "%s\t%s\n", name, snippets[name])
	}
	return 0
}

func runSnippetDelete(args []string, deps Deps) int {
	fs := newFlagSet("whop snippet delete", deps)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop snippet delete <name>")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if err := metaStore.DeleteSnippet(fs.Arg(0)); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "snippet %s deleted\n", fs.Arg(0))
	return 0
}

func runSnippetRun(args []string, deps Deps) int {
	fs := newFlagSet("whop snippet run", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop snippet run [flags] <name> <alias...>")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	snippets, err := metaStore.Snippets()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	name := fs.Arg(0)
	command, ok := snippets[name]
	if !ok {
		fmt.Fprintf(deps.Stderr, "whop: snippet %q not found\n", name)
		return 1
	}
	hosts, err := loadHosts(deps, metaStore, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	failed := false
	for _, alias := range fs.Args()[1:] {
		host, ok := sshconfig.Find(hosts, alias)
		if !ok {
			fmt.Fprintf(deps.Stderr, "whop: host %q not found\n", alias)
			failed = true
			continue
		}
		fmt.Fprintf(deps.Stdout, "== %s ==\n", alias)
		output, err := deps.RunCommand(host, hosts, command, deps.UseAgent)
		if output != "" {
			fmt.Fprint(deps.Stdout, output)
			if !strings.HasSuffix(output, "\n") {
				fmt.Fprintln(deps.Stdout)
			}
		}
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %s: %v\n", alias, err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func writeDiagnostic(deps Deps, host string, err error, asJSON bool) {
	diagnostic := sshconn.Diagnose(host, err)
	if asJSON {
		encoded, encodeErr := json.Marshal(diagnostic)
		if encodeErr != nil {
			fmt.Fprintf(deps.Stderr, "{\"error\":\"encode diagnostic: %s\"}\n", encodeErr)
			return
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
		return
	}
	fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
	if diagnostic.Stage != "" {
		fmt.Fprintf(deps.Stderr, "  stage: %s\n  category: %s\n", diagnostic.Stage, diagnostic.Category)
	}
	if diagnostic.Hint != "" {
		fmt.Fprintf(deps.Stderr, "  hint: %s\n", diagnostic.Hint)
	}
}

func runDiagnose(args []string, deps Deps) int {
	fs := newFlagSet("whop diagnose", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop diagnose [flags] <alias>")
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
	if err := deps.Diagnose(host, hosts, deps.UseAgent); err != nil {
		writeDiagnostic(deps, host.Alias, err, *asJSON)
		return 1
	}
	if *asJSON {
		diagnostic := sshconn.Diagnostic{Stage: "handshake", Category: "ok", Host: host.Alias, Message: "SSH handshake and authentication succeeded"}
		encoded, _ := json.Marshal(diagnostic)
		fmt.Fprintln(deps.Stdout, string(encoded))
	} else {
		fmt.Fprintf(deps.Stdout, "ok: %s SSH handshake and authentication succeeded\n", host.Alias)
	}
	return 0
}

func defaultDiagnose(host sshconfig.Host, hosts []sshconfig.Host, useAgent bool, password sshconn.PasswordProvider) error {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	client, err := connector.Dial(context.Background(), host)
	if err != nil {
		return err
	}
	return client.Close()
}

func defaultRunCommand(host sshconfig.Host, hosts []sshconfig.Host, command string, useAgent bool, password sshconn.PasswordProvider) (string, error) {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	output, err := connector.RunCommand(context.Background(), host, command)
	return string(output), err
}

func defaultRunMacro(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, macro automation.Macro, stdout, stderr io.Writer, onEvent automation.EventFunc, useAgent bool, password sshconn.PasswordProvider) error {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	return connector.RunInteractive(ctx, host, stdout, stderr, func(stream io.ReadWriter) error {
		return automation.RunMacro(ctx, stream, macro, automation.Options{CloseStream: true, OnEvent: onEvent})
	})
}

func runZmodem(args []string, deps Deps) int {
	if len(args) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop zmodem <put|get> [flags] <alias> <path...>")
		return 2
	}
	switch args[0] {
	case "put", "upload":
		return runZmodemPut(args[1:], deps)
	case "get", "download":
		return runZmodemGet(args[1:], deps)
	default:
		fmt.Fprintf(deps.Stderr, "whop: unknown zmodem command %q\n", args[0])
		return 2
	}
}

func runZmodemPut(args []string, deps Deps) int {
	fs := newFlagSet("whop zmodem put", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	overwrite := fs.Bool("overwrite", false, "overwrite remote files")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop zmodem put [flags] <alias> <file...>")
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
	if err := deps.ZmodemPut(host, hosts, fs.Args()[1:], *overwrite, deps.UseAgent, deps.Stdout); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: zmodem put: %v\n", err)
		return 1
	}
	return 0
}

func runZmodemGet(args []string, deps Deps) int {
	fs := newFlagSet("whop zmodem get", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	dest := fs.String("dest", "", "local destination directory")
	overwrite := fs.Bool("overwrite", false, "overwrite local files")
	maxSize := fs.Int64("max-size", 0, "maximum accepted file size (bytes, 0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop zmodem get [flags] <alias> <remote-file...>")
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
	destination := *dest
	if destination == "" {
		destination = defaultDownloadDir(host.Alias)
	}
	if err := deps.ZmodemGet(host, hosts, fs.Args()[1:], destination, *maxSize, *overwrite, deps.UseAgent, deps.Stdout); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: zmodem get: %v\n", err)
		return 1
	}
	return 0
}

func defaultZmodemPut(host sshconfig.Host, hosts []sshconfig.Host, files []string, overwrite bool, useAgent bool, out io.Writer, password sshconn.PasswordProvider) error {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	command := "rz"
	if overwrite {
		command = "rz -y"
	}
	ctx := context.Background()
	return connector.RunZmodem(ctx, host, command, func(stream io.ReadWriter) error {
		return zmodem.Send(ctx, stream, files, zmodem.ProgressPrinter(out))
	})
}

func defaultZmodemGet(host sshconfig.Host, hosts []sshconfig.Host, remoteFiles []string, dest string, maxSize int64, overwrite bool, useAgent bool, out io.Writer, password sshconn.PasswordProvider) error {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	quoted := make([]string, len(remoteFiles))
	for i, file := range remoteFiles {
		quoted[i] = sshconn.ShellQuote(file)
	}
	command := "sz -b " + strings.Join(quoted, " ")
	ctx := context.Background()
	return connector.RunZmodem(ctx, host, command, func(stream io.ReadWriter) error {
		_, err := zmodem.Receive(ctx, stream, dest, maxSize, overwrite, zmodem.ProgressPrinter(out))
		return err
	})
}

func defaultDownloadDir(alias string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, alias)
	return filepath.Join(home, "Downloads", "whop", name)
}

func defaultConnect(host sshconfig.Host, hosts []sshconfig.Host, stdin io.Reader, stdout, stderr io.Writer, record bool, useAgent bool, password sshconn.PasswordProvider) error {
	runner := sshRunnerWithHosts(hosts, record, defaultRecordingDir(), useAgent, password)
	return runner.Run(context.Background(), host, stdin, stdout, stderr)
}

func defaultPrepareConnect(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, record, useAgent bool, password sshconn.PasswordProvider) (PreparedConnect, error) {
	runner := sshRunnerWithHosts(hosts, record, defaultRecordingDir(), useAgent, sshconn.PromptlessPasswordProvider(password))
	return runner.Prepare(ctx, host)
}

func sshRunnerForConfig(path string, record bool, recordingDir string, useAgent bool, password sshconn.PasswordProvider) *sshconn.Runner {
	hosts, _ := sshconfig.Load(path)
	return sshRunnerWithHosts(hosts, record, recordingDir, useAgent, password)
}

func sshConnectorWithHosts(hosts []sshconfig.Host, useAgent bool, password sshconn.PasswordProvider) *sshconn.Connector {
	byAlias := make(map[string]sshconfig.Host, len(hosts))
	for _, host := range hosts {
		byAlias[host.Alias] = host
	}
	if password == nil {
		password = sshconn.TerminalPasswordProvider{In: os.Stdin, Out: os.Stderr}
	}
	return sshconn.New(sshconn.Options{
		Password:     password,
		DisableAgent: !useAgent,
		ResolveHost: func(alias string) (sshconfig.Host, bool) {
			host, ok := byAlias[alias]
			return host, ok
		},
	})
}

func sshRunnerWithHosts(hosts []sshconfig.Host, record bool, recordingDir string, useAgent bool, password sshconn.PasswordProvider) *sshconn.Runner {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	runner := sshconn.NewRunner(connector, nil)
	configureAutoZmodem(runner)
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
	return filepath.Join(dir, "whop", "state.db")
}

func defaultRecordingDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "whop", "recordings")
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
