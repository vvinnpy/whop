package nccm

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRealNCCMFragment(t *testing.T) {
	t.Parallel()

	data := []byte(`
nccm_config_controlmode: std
nccm_config_keepalive: 30
nccm_config_identity: false
nccm_keybindings:
  nccm_key_enter:
    - 10
    - 13

10.215.211.232:
  address: 10.215.211.232
  user: root
  keepalive: 40
  identity: /home/k/Tools/develop_wh.pem
  customargs: -z ssh-rsa

10.215.211.141:
  comment: static-web, nginx
  address: 10.215.211.141
  user: root
  keepalive: 40
  identity: /home/k/Tools/develop_wh.pem

Lab server:
  address: 10.2.2.2
  port: 11222
  comment: this is another comment
  user: foo

"user@example.com: restricted": {}
`)

	config, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := Config{Connections: []Connection{
		{
			Name:       "10.215.211.232",
			Address:    "10.215.211.232",
			User:       "root",
			Identity:   "/home/k/Tools/develop_wh.pem",
			Keepalive:  40,
			CustomArgs: "-z ssh-rsa",
		},
		{
			Name:      "10.215.211.141",
			Address:   "10.215.211.141",
			User:      "root",
			Identity:  "/home/k/Tools/develop_wh.pem",
			Comment:   "static-web, nginx",
			Keepalive: 40,
		},
		{
			Name:    "Lab server",
			Address: "10.2.2.2",
			User:    "foo",
			Port:    11222,
			Comment: "this is another comment",
		},
		{
			Name: "user@example.com: restricted",
		},
	}}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("Parse() = %#v, want %#v", config, want)
	}
}

func TestParseFiltersGlobalsAndNonMappings(t *testing.T) {
	t.Parallel()

	data := []byte(`
nccm_config_loglevel: info
nccm_keybindings:
  nccm_key_tab: [9]
nccm_loop_nccm: false
ignored scalar: ignored
ignored sequence:
  - one
  - two
empty connection: {}

ordered-a:
  address: a.example

nccm_config_unknown:
  nested: true
ordered-b:
  address: b.example
`)

	config, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	gotNames := make([]string, 0, len(config.Connections))
	for _, connection := range config.Connections {
		gotNames = append(gotNames, connection.Name)
	}
	wantNames := []string{"empty connection", "ordered-a", "ordered-b"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("connection order = %#v, want %#v", gotNames, wantNames)
	}
	if config.Connections[0].Address != "" || config.Connections[0].User != "" {
		t.Fatalf("empty connection fields were not preserved: %#v", config.Connections[0])
	}
}

func TestParseNCCMNameFormsLeaveAddressAndUserEmpty(t *testing.T) {
	t.Parallel()

	data := []byte(`
root@10.10.1.1: {}
10.10.2.2: {}
server.example.com: {}
user@example2.com:
  comment: Comment supplied, user/server extracted from FriendlyName
`)

	config, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []Connection{
		{Name: "root@10.10.1.1"},
		{Name: "10.10.2.2"},
		{Name: "server.example.com"},
		{Name: "user@example2.com", Comment: "Comment supplied, user/server extracted from FriendlyName"},
	}
	if !reflect.DeepEqual(config.Connections, want) {
		t.Fatalf("Parse() = %#v, want %#v", config.Connections, want)
	}
}

func TestParseCustomArgsPreservesPasswordWithoutSpecialHandling(t *testing.T) {
	t.Parallel()

	const args = `-P "'U4*9jCv$#D2D5yt'" -z expect`
	config, err := Parse([]byte("10.215.211.31:\n  customargs: " + args + "\n"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(config.Connections) != 1 {
		t.Fatalf("got %d connections, want 1", len(config.Connections))
	}
	if got := config.Connections[0].CustomArgs; got != args {
		t.Fatalf("CustomArgs = %q, want %q", got, args)
	}
}

func TestParseCommentsIdentityAndCustomArgs(t *testing.T) {
	t.Parallel()

	data := []byte(`
border router:
  comment: My border router   # trailing comment
  address: 192.168.1.1
  user: admin
  keepalive: 0
  identity: /home/user/mykey
  customargs: -v -C -p 2222
`)

	config, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := Connection{
		Name:       "border router",
		Address:    "192.168.1.1",
		User:       "admin",
		Identity:   "/home/user/mykey",
		Comment:    "My border router",
		Keepalive:  0,
		CustomArgs: "-v -C -p 2222",
	}
	if !reflect.DeepEqual(config.Connections, []Connection{want}) {
		t.Fatalf("Parse() = %#v, want %#v", config.Connections, []Connection{want})
	}
}

func TestParseEmptyDocument(t *testing.T) {
	t.Parallel()

	for _, data := range []string{"", "# comments only\n", "null\n"} {
		config, err := Parse([]byte(data))
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", data, err)
		}
		if len(config.Connections) != 0 {
			t.Fatalf("Parse(%q) returned %d connections, want 0", data, len(config.Connections))
		}
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "invalid yaml", data: "connection: [\n", want: "parse nccm yaml"},
		{name: "root sequence", data: "- one\n- two\n", want: "root must be a mapping"},
		{name: "empty name", data: "\"  \": {}\n", want: "connection name must not be empty"},
		{name: "port zero", data: "host:\n  port: 0\n", want: "port must be between 1 and 65535"},
		{name: "port negative", data: "host:\n  port: -1\n", want: "port must be between 1 and 65535"},
		{name: "port too large", data: "host:\n  port: 65536\n", want: "port must be between 1 and 65535"},
		{name: "port wrong type", data: "host:\n  port: nope\n", want: `nccm connection "host"`},
		{name: "keepalive negative", data: "host:\n  keepalive: -1\n", want: "keepalive must not be negative"},
		{name: "keepalive wrong type", data: "host:\n  keepalive: false\n", want: `nccm connection "host"`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(test.data))
			if err == nil {
				t.Fatal("Parse() error = nil, want error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %q, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nccm.yml")
	if err := os.WriteFile(path, []byte("prod:\n  address: prod.example\n  user: deploy\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(config.Connections) != 1 || config.Connections[0].Address != "prod.example" {
		t.Fatalf("Load() = %#v", config)
	}

	_, err = Load(filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) error = %v, want os.ErrNotExist", err)
	}
}

func TestDefaultPathUsesNCCMSearchOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".config", "nccm", "nccm.yml")
	dotPath := filepath.Join(home, ".nccm.yml")
	homePath := filepath.Join(home, "nccm.yml")
	for _, path := range []string{configPath, dotPath, homePath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if got != configPath {
		t.Fatalf("DefaultPath() = %q, want %q", got, configPath)
	}

	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	got, err = DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() after removing first path error = %v", err)
	}
	if got != dotPath {
		t.Fatalf("DefaultPath() = %q, want %q", got, dotPath)
	}

	if err := os.Remove(dotPath); err != nil {
		t.Fatal(err)
	}
	got, err = DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() after removing second path error = %v", err)
	}
	if got != homePath {
		t.Fatalf("DefaultPath() = %q, want %q", got, homePath)
	}
}

func TestDefaultPathIgnoresDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A directory at an earlier candidate must not hide a later regular file.
	if err := os.MkdirAll(filepath.Join(home, ".config", "nccm", "nccm.yml"), 0o700); err != nil {
		t.Fatal(err)
	}
	dotPath := filepath.Join(home, ".nccm.yml")
	if err := os.WriteFile(dotPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if got != dotPath {
		t.Fatalf("DefaultPath() = %q, want %q", got, dotPath)
	}
}
