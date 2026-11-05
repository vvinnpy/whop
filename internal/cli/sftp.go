package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	coreconnector "whop/internal/connector"
	sshconn "whop/internal/connector/ssh"
	"whop/internal/sshconfig"
	"whop/internal/ui"
)

type sftpRequest struct {
	host sshconfig.Host
}

func runSFTP(args []string, deps Deps) int {
	fs := newFlagSet("whop sftp", deps)
	configPath := fs.String("config", defaultConfigPath(), "path to ssh config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(deps.Stderr, "usage: whop sftp [flags] <alias>")
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
	if err := deps.RunSFTP(host, hosts, deps.UseAgent); err != nil {
		fmt.Fprintf(deps.Stderr, "whop: sftp: %v\n", err)
		return 1
	}
	return 0
}

func defaultRunSFTP(host sshconfig.Host, hosts []sshconfig.Host, useAgent bool, password sshconn.PasswordProvider) error {
	connector := sshConnectorWithHosts(hosts, useAgent, password)
	model := ui.NewSFTP(ui.SFTPOptions{
		Host: host,
		OpenRemote: func(ctx context.Context, target sshconfig.Host) (coreconnector.FileTransfer, error) {
			return connector.OpenFileTransfer(ctx, target)
		},
	})
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, runErr := program.Run()
	closeErr := model.Close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}
