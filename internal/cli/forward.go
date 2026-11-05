package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"

	coreconnector "whop/internal/connector"
	sshconn "whop/internal/connector/ssh"
	"whop/internal/sshconfig"
)

type stringList []string

type forwardRequest struct {
	host sshconfig.Host
	spec coreconnector.ForwardSpec
}

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func runForward(args []string, deps Deps) int {
	fs := newFlagSet("whop forward", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	var localValues, remoteValues, dynamicValues stringList
	fs.Var(&localValues, "L", "local forward (ssh -L): [bind:]port:host:port")
	fs.Var(&remoteValues, "R", "remote forward (ssh -R): [bind:]port:host:port")
	fs.Var(&dynamicValues, "D", "dynamic SOCKS5 forward (ssh -D): [bind:]port")
	asJSON := fs.Bool("json", false, "emit forwarding status as JSON lines")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop forward [-L spec] [-R spec] [-D spec] <alias>")
		return 2
	}
	specs, err := parseForwardValues(localValues, remoteValues, dynamicValues)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "whop: %v\n", err)
		return 2
	}
	if len(specs) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: whop forward [-L spec] [-R spec] [-D spec] <alias>")
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := runForwardSpecs(ctx, deps, host, hosts, specs, *asJSON); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: forward: %v\n", err)
		return 1
	}
	return 0
}

func runForwardInteractive(deps Deps, host sshconfig.Host, hosts []sshconfig.Host, spec coreconnector.ForwardSpec) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runForwardSpecs(ctx, deps, host, hosts, []coreconnector.ForwardSpec{spec}, false)
}

func runForwardSpecs(ctx context.Context, deps Deps, host sshconfig.Host, hosts []sshconfig.Host, specs []coreconnector.ForwardSpec, asJSON bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var outputMu sync.Mutex
	ready := func(status coreconnector.ForwardStatus) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		if asJSON {
			encoded, err := json.Marshal(status)
			if err != nil {
				return err
			}
			fmt.Fprintln(deps.Stdout, string(encoded))
			return nil
		}
		if status.DestinationAddress != "" {
			fmt.Fprintf(deps.Stdout, "%s -> %s (%s)\n", status.ListenAddress, status.DestinationAddress, status.Kind)
		} else {
			fmt.Fprintf(deps.Stdout, "%s (SOCKS5, %s)\n", status.ListenAddress, status.Kind)
		}
		return nil
	}

	errs := make(chan error, len(specs))
	for _, spec := range specs {
		spec := spec
		go func() {
			errs <- deps.Forward(ctx, host, hosts, spec, deps.UseAgent, ready)
		}()
	}

	firstErr := <-errs
	cancel()
	for range len(specs) - 1 {
		<-errs
	}
	if firstErr != nil && !errors.Is(firstErr, context.Canceled) {
		return firstErr
	}
	return nil
}

func parseForwardValues(localValues, remoteValues, dynamicValues []string) ([]coreconnector.ForwardSpec, error) {
	specs := make([]coreconnector.ForwardSpec, 0, len(localValues)+len(remoteValues)+len(dynamicValues))
	for _, raw := range localValues {
		spec, err := coreconnector.ParseForwardSpec(coreconnector.ForwardLocal, raw)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	for _, raw := range remoteValues {
		spec, err := coreconnector.ParseForwardSpec(coreconnector.ForwardRemote, raw)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	for _, raw := range dynamicValues {
		spec, err := coreconnector.ParseForwardSpec(coreconnector.ForwardDynamic, raw)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func defaultForward(ctx context.Context, host sshconfig.Host, hosts []sshconfig.Host, spec coreconnector.ForwardSpec, useAgent bool, ready func(coreconnector.ForwardStatus) error, password sshconn.PasswordProvider) error {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	return connector.Forward(ctx, host, spec, ready)
}
