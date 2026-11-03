package sshconn

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"ttydeck/internal/sshconfig"
)

// TerminalPasswordProvider prompts on the controlling terminal.
type TerminalPasswordProvider struct {
	In  *os.File
	Out io.Writer
}

// Password implements PasswordProvider.
func (p TerminalPasswordProvider) Password(host sshconfig.Host) (string, error) {
	return p.readSecret(fmt.Sprintf("%s password: ", host.Alias))
}

// KeyboardInteractive implements KeyboardInteractiveProvider.
func (p TerminalPasswordProvider) KeyboardInteractive(host sshconfig.Host, name, instruction string, questions []string, echos []bool) ([]string, error) {
	answers := make([]string, len(questions))
	for i, question := range questions {
		label := question
		if label == "" {
			label = name
		}
		if label == "" {
			label = "response"
		}
		var (
			answer string
			err    error
		)
		if i < len(echos) && echos[i] {
			answer, err = p.readEchoed(label + ": ")
		} else {
			answer, err = p.readSecret(label + ": ")
		}
		if err != nil {
			return nil, err
		}
		answers[i] = answer
	}
	return answers, nil
}

func (p TerminalPasswordProvider) readSecret(prompt string) (string, error) {
	in := p.In
	if in == nil {
		in = os.Stdin
	}
	out := p.Out
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprint(out, prompt)
	value, err := term.ReadPassword(int(in.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func (p TerminalPasswordProvider) readEchoed(prompt string) (string, error) {
	in := p.In
	if in == nil {
		in = os.Stdin
	}
	out := p.Out
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprint(out, prompt)
	value, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(value, "\r\n"), nil
}
