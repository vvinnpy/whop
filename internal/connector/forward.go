package connector

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ForwardKind identifies one supported port-forwarding mode.
type ForwardKind string

const (
	ForwardLocal   ForwardKind = "local"
	ForwardRemote  ForwardKind = "remote"
	ForwardDynamic ForwardKind = "dynamic"
)

// ForwardSpec describes one local, remote, or dynamic forwarding rule.
type ForwardSpec struct {
	Kind            ForwardKind `json:"kind"`
	BindHost        string      `json:"bind_host,omitempty"`
	BindPort        int         `json:"bind_port"`
	DestinationHost string      `json:"destination_host,omitempty"`
	DestinationPort int         `json:"destination_port,omitempty"`
}

// ForwardStatus reports a successfully started forwarding listener.
type ForwardStatus struct {
	Kind               ForwardKind `json:"kind"`
	ListenAddress      string      `json:"listen_address"`
	DestinationAddress string      `json:"destination_address,omitempty"`
}

// BindAddress returns a host:port suitable for net.Listen and SSH remote
// forwarding requests.
func (s ForwardSpec) BindAddress() string {
	return net.JoinHostPort(s.BindHost, strconv.Itoa(s.BindPort))
}

// DestinationAddress returns the forwarding destination.
func (s ForwardSpec) DestinationAddress() string {
	return net.JoinHostPort(s.DestinationHost, strconv.Itoa(s.DestinationPort))
}

// Validate checks whether the forwarding rule is complete and safe to start.
func (s ForwardSpec) Validate() error {
	switch s.Kind {
	case ForwardLocal, ForwardRemote:
		if strings.TrimSpace(s.DestinationHost) == "" {
			return errors.New("connector: forwarding destination host is empty")
		}
		if s.DestinationPort < 1 || s.DestinationPort > 65535 {
			return fmt.Errorf("connector: forwarding destination port %d is invalid", s.DestinationPort)
		}
	case ForwardDynamic:
		if s.DestinationHost != "" || s.DestinationPort != 0 {
			return errors.New("connector: dynamic forwarding cannot have a destination")
		}
	default:
		return fmt.Errorf("connector: unknown forwarding kind %q", s.Kind)
	}
	if strings.TrimSpace(s.BindHost) == "" {
		return errors.New("connector: forwarding bind host is empty")
	}
	if s.BindPort < 0 || s.BindPort > 65535 {
		return fmt.Errorf("connector: forwarding bind port %d is invalid", s.BindPort)
	}
	return nil
}

// ParseForwardSpec parses OpenSSH-like -L/-R/-D values. Local and remote use
// [bind:]port:host:port; dynamic uses [bind:]port.
func ParseForwardSpec(kind ForwardKind, raw string) (ForwardSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ForwardSpec{}, errors.New("connector: empty forwarding specification")
	}
	switch kind {
	case ForwardDynamic:
		bindHost, bindPort, err := parseBind(raw)
		if err != nil {
			return ForwardSpec{}, err
		}
		spec := ForwardSpec{Kind: kind, BindHost: bindHost, BindPort: bindPort}
		return spec, spec.Validate()
	case ForwardLocal, ForwardRemote:
		last := lastColonOutsideBrackets(raw)
		if last <= 0 || last == len(raw)-1 {
			return ForwardSpec{}, fmt.Errorf("connector: %s forwarding needs [bind:]port:host:port", kind)
		}
		destPort, err := parsePort(raw[last+1:], "destination")
		if err != nil {
			return ForwardSpec{}, err
		}
		left := raw[:last]
		split := lastColonOutsideBrackets(left)
		if split <= 0 || split == len(left)-1 {
			return ForwardSpec{}, fmt.Errorf("connector: %s forwarding needs [bind:]port:host:port", kind)
		}
		destHost := unbracket(left[split+1:])
		if destHost == "" {
			return ForwardSpec{}, errors.New("connector: forwarding destination host is empty")
		}
		bindHost, bindPort, err := parseBind(left[:split])
		if err != nil {
			return ForwardSpec{}, err
		}
		spec := ForwardSpec{
			Kind:            kind,
			BindHost:        bindHost,
			BindPort:        bindPort,
			DestinationHost: destHost,
			DestinationPort: destPort,
		}
		return spec, spec.Validate()
	default:
		return ForwardSpec{}, fmt.Errorf("connector: unknown forwarding kind %q", kind)
	}
}

func parseBind(raw string) (host string, port int, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, errors.New("connector: empty bind address")
	}
	if strings.HasPrefix(raw, "[") {
		host, portText, splitErr := net.SplitHostPort(raw)
		if splitErr != nil {
			return "", 0, fmt.Errorf("connector: parse bind address %q: %w", raw, splitErr)
		}
		port, err = parsePort(portText, "bind")
		if err != nil {
			return "", 0, err
		}
		return host, port, nil
	}
	if strings.Count(raw, ":") > 1 {
		return "", 0, fmt.Errorf("connector: IPv6 bind address %q must use brackets", raw)
	}
	if colon := strings.LastIndex(raw, ":"); colon >= 0 {
		if colon == 0 {
			host = "127.0.0.1"
		} else {
			host = raw[:colon]
		}
		port, err = parsePort(raw[colon+1:], "bind")
		if err != nil {
			return "", 0, err
		}
		return host, port, nil
	}
	port, err = parsePort(raw, "bind")
	if err != nil {
		return "", 0, err
	}
	return "127.0.0.1", port, nil
}

func parsePort(raw, label string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 0 || port > 65535 {
		return 0, fmt.Errorf("connector: %s port %q is invalid", label, raw)
	}
	return port, nil
}

func lastColonOutsideBrackets(value string) int {
	depth := 0
	last := -1
	for i, r := range value {
		switch r {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				last = i
			}
		}
	}
	return last
}

func unbracket(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		return value[1 : len(value)-1]
	}
	return value
}
