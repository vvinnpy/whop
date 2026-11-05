package sshconn

import (
	"errors"
	"fmt"

	"whop/internal/sshconfig"
)

// SecretLookup returns a stored password for a host. found=false means the
// caller should fall back to an interactive provider.
type SecretLookup func(host sshconfig.Host) (secret string, found bool, err error)

// StoredPasswordProvider prefers a locally stored password and falls back to
// the normal interactive provider when no stored secret exists. Lookup errors,
// such as an invalid vault master password, are returned instead of silently
// prompting for a host password.
type StoredPasswordProvider struct {
	Lookup   SecretLookup
	Fallback PasswordProvider
	// NonInteractiveLookup is used by Promptless. It must return
	// ErrInteractiveAuthRequired instead of prompting.
	NonInteractiveLookup SecretLookup
}

// Promptless returns a provider that never reads from the terminal. It is used
// while the caller's own UI still owns stdin; a missing secret is reported as
// ErrInteractiveAuthRequired so the caller can hand over the terminal.
func (p *StoredPasswordProvider) Promptless() PasswordProvider {
	if p == nil {
		return nil
	}
	lookup := p.NonInteractiveLookup
	if lookup == nil {
		lookup = func(sshconfig.Host) (string, bool, error) {
			return "", false, ErrInteractiveAuthRequired
		}
	}
	return &StoredPasswordProvider{
		Lookup:   lookup,
		Fallback: promptRequiredPasswordProvider{},
	}
}

type promptRequiredPasswordProvider struct{}

func (promptRequiredPasswordProvider) Password(sshconfig.Host) (string, error) {
	return "", ErrInteractiveAuthRequired
}

func (promptRequiredPasswordProvider) KeyboardInteractive(sshconfig.Host, string, string, []string, []bool) ([]string, error) {
	return nil, ErrInteractiveAuthRequired
}

// PromptlessPasswordProvider removes terminal prompting from provider. Unknown
// providers are treated as interactive because they cannot prove otherwise.
func PromptlessPasswordProvider(provider PasswordProvider) PasswordProvider {
	if provider == nil {
		return nil
	}
	if promptless, ok := provider.(interface{ Promptless() PasswordProvider }); ok {
		return promptless.Promptless()
	}
	return promptRequiredPasswordProvider{}
}

// Password implements PasswordProvider.
func (p *StoredPasswordProvider) Password(host sshconfig.Host) (string, error) {
	if p == nil {
		return "", errors.New("ssh: nil stored password provider")
	}
	if p.Lookup != nil {
		secret, found, err := p.Lookup(host)
		if err != nil {
			return "", err
		}
		if found {
			return secret, nil
		}
	}
	if p.Fallback != nil {
		return p.Fallback.Password(host)
	}
	return "", errors.New("ssh: no password provider configured")
}

// KeyboardInteractive implements KeyboardInteractiveProvider. A stored secret
// is used only for the common single-password challenge; more complex
// challenges fall back interactively so a password is never mistaken for OTP
// or another prompt.
func (p *StoredPasswordProvider) KeyboardInteractive(host sshconfig.Host, name, instruction string, questions []string, echos []bool) ([]string, error) {
	if p == nil {
		return nil, errors.New("ssh: nil stored password provider")
	}
	if p.Lookup != nil {
		secret, found, err := p.Lookup(host)
		if err != nil {
			return nil, err
		}
		if found && len(questions) == 1 && (len(echos) == 0 || !echos[0]) {
			return []string{secret}, nil
		}
	}
	if fallback, ok := p.Fallback.(KeyboardInteractiveProvider); ok {
		return fallback.KeyboardInteractive(host, name, instruction, questions, echos)
	}
	return nil, fmt.Errorf("ssh: keyboard-interactive fallback is unavailable")
}
