// Package nccm reads the connection subset of nccm's YAML configuration.
package nccm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Connection is one nccm connection entry.
//
// Address and User may be empty for nccm entries whose Name is already in
// user@host or host form. Name is always the original top-level YAML key.
type Connection struct {
	Name       string
	Address    string
	User       string
	Identity   string
	Comment    string
	Port       int
	Keepalive  int
	CustomArgs string
}

// Config is the parsed nccm configuration.
type Config struct {
	Connections []Connection
}

type rawConnection struct {
	Address    string `yaml:"address"`
	User       string `yaml:"user"`
	Identity   string `yaml:"identity"`
	Comment    string `yaml:"comment"`
	Port       *int   `yaml:"port"`
	Keepalive  *int   `yaml:"keepalive"`
	CustomArgs string `yaml:"customargs"`
}

// Parse parses nccm YAML data while preserving connection file order.
//
// Top-level nccm_* settings and top-level values that are not mappings are
// ignored. These rules let a complete nccm configuration be passed directly
// without trying to model nccm's global settings.
func Parse(data []byte) (Config, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return Config{}, fmt.Errorf("parse nccm yaml: %w", err)
	}

	config := Config{Connections: make([]Connection, 0)}
	if len(document.Content) == 0 {
		return config, nil
	}

	root := document.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return config, nil
	}
	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("nccm yaml root must be a mapping")
	}

	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		value := root.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return Config{}, fmt.Errorf("nccm yaml line %d: connection name must be a scalar", key.Line)
		}

		name := key.Value
		if strings.HasPrefix(name, "nccm_") {
			continue
		}

		value = resolveAlias(value)
		if value.Kind != yaml.MappingNode {
			continue
		}
		if strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("nccm yaml line %d: connection name must not be empty", key.Line)
		}

		connection, err := parseConnection(name, value)
		if err != nil {
			return Config{}, err
		}
		config.Connections = append(config.Connections, connection)
	}

	return config, nil
}

func parseConnection(name string, value *yaml.Node) (Connection, error) {
	var raw rawConnection
	if err := value.Decode(&raw); err != nil {
		return Connection{}, fmt.Errorf("nccm connection %q: %w", name, err)
	}

	port := 0
	if raw.Port != nil {
		port = *raw.Port
		if port < 1 || port > 65535 {
			return Connection{}, fmt.Errorf("nccm connection %q: port must be between 1 and 65535", name)
		}
	}

	keepalive := 0
	if raw.Keepalive != nil {
		keepalive = *raw.Keepalive
		if keepalive < 0 {
			return Connection{}, fmt.Errorf("nccm connection %q: keepalive must not be negative", name)
		}
	}

	return Connection{
		Name:       name,
		Address:    raw.Address,
		User:       raw.User,
		Identity:   raw.Identity,
		Comment:    raw.Comment,
		Port:       port,
		Keepalive:  keepalive,
		CustomArgs: raw.CustomArgs,
	}, nil
}

func resolveAlias(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

// Load reads and parses the nccm configuration at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("load nccm config %q: %w", path, err)
	}
	config, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("load nccm config %q: %w", path, err)
	}
	return config, nil
}

// DefaultPath returns the first existing nccm configuration file in nccm's
// standard search order.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	candidates := []string{
		filepath.Join(home, ".config", "nccm", "nccm.yml"),
		filepath.Join(home, ".nccm.yml"),
		filepath.Join(home, "nccm.yml"),
		"/etc/nccm.yml",
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			return path, nil
		case err == nil:
			continue
		case errors.Is(err, fs.ErrNotExist):
			continue
		default:
			return "", fmt.Errorf("inspect nccm config %q: %w", path, err)
		}
	}

	return "", fmt.Errorf("nccm config not found in %s", strings.Join(candidates, ", "))
}
