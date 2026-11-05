package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type helpFlag struct {
	Name        string
	Description string
}

type helpEnv struct {
	Name        string
	Description string
}

type helpCommand struct {
	Name        string
	Aliases     []string
	Usage       string
	Summary     string
	Description string
	Flags       []helpFlag
	Env         []helpEnv
	Commands    []helpCommand
	Examples    []string
}

type helpGroup struct {
	Title    string
	Commands []helpCommand
}

var helpGroups = []helpGroup{
	{
		Title: "Connection",
		Commands: []helpCommand{
			{
				Name:    "list",
				Usage:   "whop list [flags]",
				Summary: "List hosts from the SSH config",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
					{"-all", "include service endpoints"},
					{"-json", "emit JSON"},
				},
				Examples: []string{"whop list", "whop list --json", "whop list --all"},
			},
			{
				Name:    "connect",
				Usage:   "whop connect [flags] <alias>",
				Summary: "Connect to a host interactively",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
					{"-record", "record the session"},
					{"-json", "emit a structured connection diagnostic on failure"},
				},
				Examples: []string{"whop connect prod-db", "whop connect --record prod-db"},
			},
			{
				Name:    "forward",
				Usage:   "whop forward [flags] <alias>",
				Summary: "Forward local, remote, or dynamic ports over SSH",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
					{"-L spec", "local forward: [bind:]port:host:port (repeatable)"},
					{"-R spec", "remote forward: [bind:]port:host:port (repeatable)"},
					{"-D spec", "dynamic SOCKS5 forward: [bind:]port (repeatable)"},
					{"-json", "emit forwarding status as JSON lines"},
				},
				Examples: []string{
					"whop forward -L 127.0.0.1:8080:db.internal:5432 prod-db",
					"whop forward -D 127.0.0.1:1080 prod-db",
				},
			},
			{
				Name:    "sftp",
				Usage:   "whop sftp [flags] <alias>",
				Summary: "Open the dual-pane SFTP file browser",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
				},
			},
			{
				Name:    "run",
				Usage:   "whop run [flags] <alias> <command...>",
				Summary: "Run a command on a remote host and print its output",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
				},
				Examples: []string{"whop run prod-db uptime"},
			},
			{
				Name:    "diagnose",
				Usage:   "whop diagnose [flags] <alias>",
				Summary: "Diagnose SSH connection and authentication failures",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
					{"-json", "emit JSON"},
				},
			},
		},
	},
	{
		Title: "Sessions",
		Commands: []helpCommand{
			{
				Name:    "record",
				Usage:   "whop record [flags] <alias> [on|off]",
				Summary: "Show or change the per-host recording policy",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
					{"-json", "emit JSON"},
				},
			},
			{
				Name:    "replay",
				Usage:   "whop replay [flags] <file.cast>",
				Summary: "Replay an asciicast v2 recording",
				Flags: []helpFlag{
					{"-speed factor", "playback speed (default 1)"},
				},
			},
			{
				Name:    "search",
				Usage:   "whop search [flags] <query>",
				Summary: "Search recorded session transcripts",
				Flags: []helpFlag{
					{"-dir path", "recording directory"},
					{"-files", "scan transcript files instead of the SQLite index"},
					{"-no-sync", "search the existing index without scanning recordings"},
					{"-skip-invalid", "skip malformed recordings while syncing"},
					{"-json", "emit JSON"},
				},
			},
			{
				Name:    "index",
				Usage:   "whop index [flags]",
				Summary: "Rebuild and prune the transcript search index",
				Flags: []helpFlag{
					{"-dir path", "recording directory"},
					{"-skip-invalid", "skip malformed recordings"},
					{"-no-prune", "keep index entries for deleted recordings"},
					{"-json", "emit JSON"},
				},
			},
			{
				Name:        "zmodem",
				Usage:       "whop zmodem <put|get> [flags] <alias> <path...>",
				Summary:     "Transfer files with ZMODEM over an SSH session",
				Description: "The remote host must provide rz/sz, usually via lrzsz.",
				Commands: []helpCommand{
					{
						Name:    "put",
						Aliases: []string{"upload"},
						Usage:   "whop zmodem put [flags] <alias> <file...>",
						Summary: "Upload local files with remote rz",
						Flags: []helpFlag{
							{"-config path", "path to ssh config (default ~/.ssh/config)"},
							{"-overwrite", "overwrite remote files"},
						},
					},
					{
						Name:    "get",
						Aliases: []string{"download"},
						Usage:   "whop zmodem get [flags] <alias> <remote-file...>",
						Summary: "Download remote files with remote sz",
						Flags: []helpFlag{
							{"-config path", "path to ssh config (default ~/.ssh/config)"},
							{"-dest path", "local destination directory"},
							{"-overwrite", "overwrite local files"},
							{"-max-size bytes", "maximum accepted file size (0 = unlimited)"},
						},
					},
				},
			},
		},
	},
	{
		Title: "Metadata and automation",
		Commands: []helpCommand{
			{
				Name:    "favorite",
				Usage:   "whop favorite [flags] <alias> <on|off>",
				Summary: "Mark or unmark a host as favorite",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
				},
			},
			{
				Name:    "access",
				Usage:   "whop access [flags] <alias> [auto|login|service]",
				Summary: "Show or set how a host is classified",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
				},
			},
			{
				Name:    "note",
				Usage:   "whop note [flags] <alias> [text...]",
				Summary: "Show or replace a host note",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
				},
			},
			{
				Name:    "tag",
				Usage:   "whop tag [flags] <alias> [tag...]",
				Summary: "Show or replace host tags",
				Flags: []helpFlag{
					{"-config path", "path to ssh config (default ~/.ssh/config)"},
				},
			},
			{
				Name:    "snippet",
				Usage:   "whop snippet <set|list|delete|run> ...",
				Summary: "Manage and run named command snippets",
				Commands: []helpCommand{
					{
						Name:    "set",
						Usage:   "whop snippet set <name> <command...>",
						Summary: "Save or replace a snippet",
					},
					{
						Name:    "list",
						Aliases: []string{"ls"},
						Usage:   "whop snippet list",
						Summary: "List saved snippets",
					},
					{
						Name:    "delete",
						Aliases: []string{"rm"},
						Usage:   "whop snippet delete <name>",
						Summary: "Delete a snippet",
					},
					{
						Name:    "run",
						Usage:   "whop snippet run [flags] <name> <alias...>",
						Summary: "Run a snippet on one or more hosts",
						Flags: []helpFlag{
							{"-config path", "path to ssh config (default ~/.ssh/config)"},
						},
					},
				},
			},
			{
				Name:    "macro",
				Usage:   "whop macro <set|list|show|delete|run> ...",
				Summary: "Manage and run expect-style automation macros",
				Commands: []helpCommand{
					{
						Name:    "set",
						Usage:   "whop macro set <name> <file|->",
						Summary: "Save or replace a macro definition",
					},
					{
						Name:    "list",
						Aliases: []string{"ls"},
						Usage:   "whop macro list [flags]",
						Summary: "List saved macros",
						Flags: []helpFlag{
							{"-json", "emit JSON"},
						},
					},
					{
						Name:    "show",
						Usage:   "whop macro show <name>",
						Summary: "Print a macro definition",
					},
					{
						Name:    "delete",
						Aliases: []string{"rm"},
						Usage:   "whop macro delete <name>",
						Summary: "Delete a macro",
					},
					{
						Name:    "run",
						Usage:   "whop macro run [flags] <name> <alias>",
						Summary: "Run a macro on a host",
						Flags: []helpFlag{
							{"-config path", "path to ssh config (default ~/.ssh/config)"},
							{"-timeout duration", "overall automation timeout (default 30s)"},
							{"-json", "emit a structured diagnostic on failure"},
						},
					},
				},
			},
			{
				Name:        "audit",
				Usage:       "whop audit [flags]",
				Summary:     "Show or clear persisted automation audit events",
				Description: "Run \"whop audit clear\" to clear the audit log.",
				Flags: []helpFlag{
					{"-limit n", "maximum events to show (default 100, max 1000)"},
					{"-json", "emit JSON"},
				},
				Commands: []helpCommand{
					{
						Name:    "clear",
						Usage:   "whop audit clear [flags]",
						Summary: "Clear the automation audit log",
						Flags: []helpFlag{
							{"-json", "emit JSON"},
						},
					},
				},
			},
		},
	},
	{
		Title: "Configuration and security",
		Commands: []helpCommand{
			{
				Name:    "import",
				Usage:   "whop import nccm [flags]",
				Summary: "Preview or apply an nccm.yml migration",
				Commands: []helpCommand{
					{
						Name:    "nccm",
						Usage:   "whop import nccm [flags]",
						Summary: "Import hosts and available passwords from nccm.yml",
						Flags: []helpFlag{
							{"-file path", "nccm.yml path (default: auto-detect)"},
							{"-config path", "destination ssh config"},
							{"-only names", "comma-separated nccm connection names"},
							{"-apply", "write changes (default: dry-run)"},
							{"-json", "emit JSON"},
						},
					},
				},
			},
			{
				Name:    "vault",
				Usage:   "whop vault <init|set|list|delete|status> ...",
				Summary: "Manage the encrypted host-password vault",
				Env: []helpEnv{
					{"WHOP_VAULT_PATH", "override the vault file path"},
					{"WHOP_VAULT_PASSWORD", "master password for automation"},
					{"WHOP_VAULT_KEY_FILE", "override the auto-unlock key file"},
					{"WHOP_HOST_PASSWORD", "host password for vault set automation"},
				},
				Commands: []helpCommand{
					{
						Name:    "init",
						Usage:   "whop vault init [flags]",
						Summary: "Create the encrypted vault",
						Flags: []helpFlag{
							{"-auto-unlock", "generate a 0600 local key file for non-interactive unlock"},
						},
					},
					{
						Name:    "set",
						Usage:   "whop vault set <alias>",
						Summary: "Store a host password in the vault",
					},
					{
						Name:    "list",
						Aliases: []string{"ls"},
						Usage:   "whop vault list [flags]",
						Summary: "List hosts with stored passwords",
						Flags: []helpFlag{
							{"-json", "emit JSON"},
						},
					},
					{
						Name:    "delete",
						Aliases: []string{"rm"},
						Usage:   "whop vault delete <alias>",
						Summary: "Delete a stored host password",
					},
					{
						Name:    "status",
						Usage:   "whop vault status [flags]",
						Summary: "Show vault path and auto-unlock status",
						Flags: []helpFlag{
							{"-json", "emit JSON"},
						},
					},
				},
			},
			{
				Name:    "version",
				Usage:   "whop version",
				Summary: "Print the whop version",
			},
		},
	},
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

func findHelpCommand(path []string) (*helpCommand, bool) {
	if len(path) == 0 {
		return nil, false
	}
	commands := allRootHelpCommands()
	var found *helpCommand
	for i, name := range path {
		var next *helpCommand
		for j := range commands {
			if helpCommandMatches(&commands[j], name) {
				next = &commands[j]
				break
			}
		}
		if next == nil {
			return nil, false
		}
		found = next
		commands = next.Commands
		if i == len(path)-1 {
			return found, true
		}
	}
	return found, found != nil
}

func allRootHelpCommands() []helpCommand {
	commands := make([]helpCommand, 0, 24)
	for _, group := range helpGroups {
		commands = append(commands, group.Commands...)
	}
	return commands
}

func helpCommandMatches(command *helpCommand, name string) bool {
	if command.Name == name {
		return true
	}
	for _, alias := range command.Aliases {
		if alias == name {
			return true
		}
	}
	return false
}

func helpRequestPath(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}
	if strings.HasPrefix(args[0], "-") {
		return nil, containsHelpArg(args)
	}
	path := []string{args[0]}
	command, ok := findHelpCommand(path)
	if !ok {
		return nil, false
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if isHelpArg(arg) {
			return path, true
		}
		if strings.HasPrefix(arg, "-") {
			if helpFlagTakesValue(arg) && !strings.Contains(arg, "=") {
				i++
			}
			continue
		}
		if len(command.Commands) == 0 {
			return nil, false
		}
		next, ok := findHelpCommand(append(append([]string(nil), path...), arg))
		if !ok {
			return nil, false
		}
		path = append(path, arg)
		command = next
	}
	return nil, false
}

func helpFlagTakesValue(arg string) bool {
	switch strings.TrimLeft(arg, "-") {
	case "config", "L", "R", "D", "dir", "speed", "limit", "timeout", "file", "only", "dest", "max-size":
		return true
	default:
		return false
	}
}

func containsHelpArg(args []string) bool {
	for _, arg := range args {
		if isHelpArg(arg) {
			return true
		}
	}
	return false
}

func runHelp(args []string, deps Deps) int {
	for len(args) > 0 && isHelpArg(args[len(args)-1]) {
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		printUsage(deps.Stdout)
		return 0
	}
	command, ok := findHelpCommand(args)
	if !ok {
		fmt.Fprintf(deps.Stderr, "whop: unknown command %q\n\n", strings.Join(args, " "))
		printUsage(deps.Stderr)
		return 2
	}
	printCommandHelp(deps.Stdout, args, command)
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `whop - fast, local-first terminal connection deck

Usage:
  whop [flags] [filter...]     open the interactive host list
  whop <command> [args...]     run a command
  whop help [command...]       show command help

Commands:`)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	first := true
	for _, group := range helpGroups {
		if !first {
			fmt.Fprintln(tw)
		}
		first = false
		fmt.Fprintf(tw, "  %s:\n", group.Title)
		for _, command := range group.Commands {
			fmt.Fprintf(tw, "    %s\t%s\n", command.Name, command.Summary)
		}
	}
	_ = tw.Flush()

	fmt.Fprintln(w, `
Interactive host list flags:
  -config path   path to ssh config (default ~/.ssh/config)
  -all           show service endpoints too
  -record        record interactive sessions
  -h, --help     show this help

Global flags:
  -agent         use ssh-agent (default: off)
  -no-agent      disable ssh-agent

Environment:
  WHOP_SSH_AGENT=1   use ssh-agent without passing -agent

Run "whop help <command>" for command-specific flags and examples.
Command flags also work with -h or --help, for example "whop connect --help".`)
}

func printCommandHelp(w io.Writer, path []string, command *helpCommand) {
	fmt.Fprintf(w, "whop %s - %s\n\n", strings.Join(path, " "), command.Summary)
	if command.Description != "" {
		fmt.Fprintf(w, "%s\n\n", command.Description)
	}
	fmt.Fprintf(w, "Usage:\n  %s\n", command.Usage)

	if len(command.Commands) > 0 {
		fmt.Fprintln(w, "\nCommands:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, child := range command.Commands {
			fmt.Fprintf(tw, "  %s\t%s\n", child.Name, child.Summary)
		}
		_ = tw.Flush()
	}

	if len(command.Flags) > 0 {
		fmt.Fprintln(w, "\nFlags:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, flag := range command.Flags {
			fmt.Fprintf(tw, "  %s\t%s\n", flag.Name, flag.Description)
		}
		_ = tw.Flush()
	}

	if len(command.Env) > 0 {
		fmt.Fprintln(w, "\nEnvironment:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, env := range command.Env {
			fmt.Fprintf(tw, "  %s\t%s\n", env.Name, env.Description)
		}
		_ = tw.Flush()
	}

	if len(command.Examples) > 0 {
		fmt.Fprintln(w, "\nExamples:")
		for _, example := range command.Examples {
			fmt.Fprintf(w, "  %s\n", example)
		}
	}

	if len(command.Commands) > 0 {
		fmt.Fprintf(w, "\nRun \"whop help %s <command>\" for command details.\n", strings.Join(path, " "))
	}
}
