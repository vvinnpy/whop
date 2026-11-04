package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"whop/internal/automation"
	"whop/internal/sshconfig"
)

const maxMacroDefinitionBytes = 1 << 20

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// macroDefinition is the portable JSON document stored in SQLite. Secret send
// steps contain only an environment variable name; secret values are resolved
// immediately before execution and never persisted.
type macroDefinition struct {
	Version int            `json:"version"`
	Name    string         `json:"name"`
	Steps   []macroStepDef `json:"steps"`
}

type macroStepDef struct {
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind"`
	Send      string `json:"send,omitempty"`
	SecretEnv string `json:"secret_env,omitempty"`
	Newline   bool   `json:"newline,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	Timeout   string `json:"timeout,omitempty"`
	Retries   int    `json:"retries,omitempty"`
	Sleep     string `json:"sleep,omitempty"`
}

func runMacro(args []string, deps Deps) int {
	if len(args) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop macro <set|list|show|delete|run> ...")
		return 2
	}
	switch args[0] {
	case "set":
		return runMacroSet(args[1:], deps)
	case "list", "ls":
		return runMacroList(args[1:], deps)
	case "show":
		return runMacroShow(args[1:], deps)
	case "delete", "rm":
		return runMacroDelete(args[1:], deps)
	case "run":
		return runMacroRun(args[1:], deps)
	default:
		fmt.Fprintf(deps.Stderr, "whop: unknown macro command %q\n", args[0])
		return 2
	}
}

func runMacroSet(args []string, deps Deps) int {
	if len(args) != 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop macro set <name> <file|->")
		return 2
	}
	name, path := strings.TrimSpace(args[0]), args[1]
	if err := validateMacroName(name); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 2
	}
	raw, err := readLimited(path, deps.Stdin, maxMacroDefinitionBytes)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: read macro: %v\n", err)
		return 1
	}
	definition, err := parseMacroDefinition(raw, name, nil)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: invalid macro: %v\n", err)
		return 1
	}
	canonical, err := json.MarshalIndent(definition, "", "  ")
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: encode macro: %v\n", err)
		return 1
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if err := metaStore.SetMacroDefinition(name, append(canonical, '\n')); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "macro %s saved (%d steps)\n", name, len(definition.Steps))
	return 0
}

func runMacroList(args []string, deps Deps) int {
	fs := newFlagSet("whop macro list", deps)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop macro list [flags]")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	definitions, err := metaStore.MacroDefinitions()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	type summary struct {
		Name  string `json:"name"`
		Steps int    `json:"steps"`
	}
	items := make([]summary, 0, len(definitions))
	invalid := make(map[string]string)
	for name, raw := range definitions {
		definition, err := parseMacroDefinition(raw, name, nil)
		if err != nil {
			invalid[name] = err.Error()
			continue
		}
		items = append(items, summary{Name: name, Steps: len(definition.Steps)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if *asJSON {
		encoded, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.Stdout, string(encoded))
	} else {
		for _, item := range items {
			fmt.Fprintf(deps.Stdout, "%s\t%d steps\n", sanitizeSingleLine(item.Name), item.Steps)
		}
	}
	if len(invalid) > 0 {
		names := make([]string, 0, len(invalid))
		for name := range invalid {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(deps.Stderr, "whop: macro %s is invalid: %s\n", sanitizeSingleLine(name), invalid[name])
		}
		return 1
	}
	return 0
}

func runMacroShow(args []string, deps Deps) int {
	if len(args) != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop macro show <name>")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	raw, found, err := metaStore.MacroDefinition(args[0])
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	if !found {
		fmt.Fprintf(deps.Stderr, "whop: macro %q not found\n", args[0])
		return 1
	}
	if _, err := parseMacroDefinition(raw, args[0], nil); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: macro %q is invalid: %v\n", args[0], err)
		return 1
	}
	fmt.Fprint(deps.Stdout, string(raw))
	if !strings.HasSuffix(string(raw), "\n") {
		fmt.Fprintln(deps.Stdout)
	}
	return 0
}

func runMacroDelete(args []string, deps Deps) int {
	if len(args) != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop macro delete <name>")
		return 2
	}
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	if err := metaStore.DeleteMacroDefinition(args[0]); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "macro %s deleted\n", args[0])
	return 0
}

func runMacroRun(args []string, deps Deps) int {
	fs := newFlagSet("whop macro run", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	timeout := fs.Duration("timeout", 30*time.Second, "overall automation timeout")
	asJSON := fs.Bool("json", false, "emit a structured diagnostic on failure")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(deps.Stderr, "usage: whop macro run [flags] <name> <alias>")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(deps.Stderr, "whop: timeout must be positive")
		return 2
	}
	name, alias := fs.Arg(0), fs.Arg(1)
	metaStore, err := deps.OpenStore()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	defer metaStore.Close()
	macro, err := loadStoredMacro(metaStore, name, deps.LookupEnv)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	hosts, err := loadHosts(deps, metaStore, resolvePath(*configPath))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 1
	}
	host, ok := sshconfig.Find(hosts, alias)
	if !ok {
		fmt.Fprintf(deps.Stderr, "whop: host %q not found\n", alias)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	collector := newAuditCollector(metaStore)
	macroAuditStarted(collector, alias, name)
	err = deps.RunMacro(ctx, host, hosts, macro, deps.Stdout, deps.Stderr, collector.automationEvent(alias, name), deps.UseAgent)
	if err != nil {
		macroAuditFailed(collector, alias, name, err)
	}
	_ = macroAuditFlush(collector, deps.Stderr)
	if err != nil {
		writeDiagnostic(deps, alias, err, *asJSON)
		return 1
	}
	_ = metaStore.RecordConnect(alias, time.Now())
	fmt.Fprintf(deps.Stderr, "macro %s completed on %s\n", name, alias)
	return 0
}

func readLimited(path string, stdin io.Reader, limit int64) ([]byte, error) {
	if path == "-" {
		if stdin == nil {
			return nil, errors.New("stdin is unavailable")
		}
		raw, err := io.ReadAll(io.LimitReader(stdin, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) > limit {
			return nil, fmt.Errorf("definition exceeds %d bytes", limit)
		}
		return raw, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("definition exceeds %d bytes", limit)
	}
	return raw, nil
}

func parseMacroDefinition(raw []byte, name string, lookupEnv func(string) (string, bool)) (macroDefinition, error) {
	var definition macroDefinition
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return macroDefinition{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return macroDefinition{}, errors.New("unexpected trailing JSON value")
		}
		return macroDefinition{}, err
	}
	if definition.Version == 0 {
		definition.Version = 1
	}
	if definition.Version != 1 {
		return macroDefinition{}, fmt.Errorf("unsupported version %d", definition.Version)
	}
	definition.Name = name
	if len(definition.Steps) == 0 {
		return macroDefinition{}, errors.New("macro has no steps")
	}
	macro, err := definition.buildMacro(lookupEnv)
	if err != nil {
		return macroDefinition{}, err
	}
	if err := macro.Validate(); err != nil {
		return macroDefinition{}, err
	}
	return definition, nil
}

func (d macroDefinition) buildMacro(lookupEnv func(string) (string, bool)) (automation.Macro, error) {
	if lookupEnv == nil {
		lookupEnv = func(string) (string, bool) { return "validation-value", true }
	}
	macro := automation.Macro{Name: d.Name, Steps: make([]automation.Step, 0, len(d.Steps))}
	for i, value := range d.Steps {
		if strings.TrimSpace(value.Name) != "" {
			if err := validateMacroName(value.Name); err != nil {
				return automation.Macro{}, fmt.Errorf("step %d: %w", i+1, err)
			}
		}
		step := automation.Step{Name: strings.TrimSpace(value.Name), Kind: automation.StepKind(strings.ToLower(strings.TrimSpace(value.Kind)))}
		switch step.Kind {
		case automation.StepSend:
			if value.Send != "" && value.SecretEnv != "" {
				return automation.Macro{}, fmt.Errorf("step %d: send and secret_env are mutually exclusive", i+1)
			}
			if value.SecretEnv != "" {
				if !envNamePattern.MatchString(value.SecretEnv) {
					return automation.Macro{}, fmt.Errorf("step %d: invalid secret_env name %q", i+1, value.SecretEnv)
				}
				secret, ok := lookupEnv(value.SecretEnv)
				if !ok || secret == "" {
					return automation.Macro{}, fmt.Errorf("step %d: environment variable %s is not set", i+1, value.SecretEnv)
				}
				step.Send = []byte(secret)
				step.Secret = true
			} else {
				step.Send = []byte(value.Send)
			}
			if value.Newline && !bytes.HasSuffix(step.Send, []byte("\n")) {
				step.Send = append(step.Send, '\n')
			}
			if value.Pattern != "" || value.Timeout != "" || value.Retries != 0 || value.Sleep != "" {
				return automation.Macro{}, fmt.Errorf("step %d: send has irrelevant wait/sleep fields", i+1)
			}
		case automation.StepWait:
			if value.Send != "" || value.SecretEnv != "" || value.Newline || value.Sleep != "" {
				return automation.Macro{}, fmt.Errorf("step %d: wait has irrelevant send/sleep fields", i+1)
			}
			step.Pattern = value.Pattern
			step.MaxRetries = value.Retries
			if value.Timeout != "" {
				parsed, err := time.ParseDuration(value.Timeout)
				if err != nil {
					return automation.Macro{}, fmt.Errorf("step %d: invalid timeout: %w", i+1, err)
				}
				step.Timeout = parsed
			}
		case automation.StepSleep:
			if value.Send != "" || value.SecretEnv != "" || value.Newline || value.Pattern != "" || value.Timeout != "" || value.Retries != 0 {
				return automation.Macro{}, fmt.Errorf("step %d: sleep has irrelevant send/wait fields", i+1)
			}
			parsed, err := time.ParseDuration(value.Sleep)
			if err != nil {
				return automation.Macro{}, fmt.Errorf("step %d: invalid sleep duration: %w", i+1, err)
			}
			step.Duration = parsed
		default:
			return automation.Macro{}, fmt.Errorf("step %d: unknown kind %q", i+1, value.Kind)
		}
		macro.Steps = append(macro.Steps, step)
	}
	return macro, nil
}

func loadMacroNames(metaStore MetaStore) ([]string, error) {
	definitions, err := metaStore.MacroDefinitions()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(definitions))
	for name, raw := range definitions {
		if err := validateMacroName(name); err != nil {
			return nil, err
		}
		if _, err := parseMacroDefinition(raw, name, nil); err != nil {
			return nil, fmt.Errorf("macro %q is invalid: %w", name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func loadStoredMacro(metaStore MetaStore, name string, lookupEnv func(string) (string, bool)) (automation.Macro, error) {
	raw, found, err := metaStore.MacroDefinition(name)
	if err != nil {
		return automation.Macro{}, err
	}
	if !found {
		return automation.Macro{}, fmt.Errorf("macro %q not found", name)
	}
	definition, err := parseMacroDefinition(raw, name, nil)
	if err != nil {
		return automation.Macro{}, fmt.Errorf("macro %q is invalid: %w", name, err)
	}
	macro, err := definition.buildMacro(lookupEnv)
	if err != nil {
		return automation.Macro{}, fmt.Errorf("macro %q is invalid: %w", name, err)
	}
	return macro, nil
}

func validateMacroName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("macro name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return errors.New("macro name exceeds 64 characters")
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return errors.New("macro name contains control characters")
		}
	}
	return nil
}
