package nccm

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseCustomArgsNCCMPasswordExample(t *testing.T) {
	t.Parallel()

	options, err := ParseCustomArgs(`-P "'U4*9jCv$#D2D5yt'" -z expect`)
	if err != nil {
		t.Fatalf("ParseCustomArgs() error = %v", err)
	}
	if !options.PasswordSet {
		t.Fatal("PasswordSet = false, want true")
	}
	if options.Password != "U4*9jCv$#D2D5yt" {
		t.Fatalf("Password = %q, want %q", options.Password, "U4*9jCv$#D2D5yt")
	}
	if options.Mode != "expect" {
		t.Fatalf("Mode = %q, want expect", options.Mode)
	}
	if len(options.Unsupported) != 0 {
		t.Fatalf("Unsupported = %#v, want none", options.Unsupported)
	}
}

func TestParseCustomArgsShellLikeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want CustomOptions
	}{
		{
			name: "single quoted password",
			raw:  `-P 'Hunter007!' -z expect`,
			want: CustomOptions{Mode: "expect", Password: "Hunter007!", PasswordSet: true},
		},
		{
			name: "password with spaces",
			raw:  `-P "correct horse battery staple" -z expect`,
			want: CustomOptions{Mode: "expect", Password: "correct horse battery staple", PasswordSet: true},
		},
		{
			name: "special characters",
			raw:  `-P 'a b$c"d!@#*()' -z expect`,
			want: CustomOptions{Mode: "expect", Password: `a b$c"d!@#*()`, PasswordSet: true},
		},
		{
			name: "escaped double quote",
			raw:  `-P "a b\$c\\d\"e" -z expect`,
			want: CustomOptions{Mode: "expect", Password: `a b$c\d"e`, PasswordSet: true},
		},
		{
			name: "nested quote layer",
			raw:  `-P "\"quoted password\"" -z expect`,
			want: CustomOptions{Mode: "expect", Password: "quoted password", PasswordSet: true},
		},
		{
			name: "identity and port",
			raw:  `-C -p 2222 -i "/home/k/keys/id with space" -z ssh-rsa`,
			want: CustomOptions{
				Mode:        "ssh-rsa",
				Compression: true,
				Port:        2222,
				Identity:    "/home/k/keys/id with space",
			},
		},
		{
			name: "all modes",
			raw:  `-z expect-ssh-rsa`,
			want: CustomOptions{Mode: "expect-ssh-rsa"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCustomArgs(test.raw)
			if err != nil {
				t.Fatalf("ParseCustomArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseCustomArgs() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseCustomArgsRepeatedOptionsLastWins(t *testing.T) {
	t.Parallel()

	options, err := ParseCustomArgs(`-P first -p 22 -i /old/key -z ssh -P 'last password' -p 2200 -i /new/key -z expect-ssh-rsa -C`)
	if err != nil {
		t.Fatalf("ParseCustomArgs() error = %v", err)
	}
	want := CustomOptions{
		Mode:        "expect-ssh-rsa",
		Password:    "last password",
		PasswordSet: true,
		Compression: true,
		Port:        2200,
		Identity:    "/new/key",
	}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("ParseCustomArgs() = %#v, want %#v", options, want)
	}
}

func TestParseCustomArgsUnknownOptionsNeverExposeValues(t *testing.T) {
	t.Parallel()

	const (
		separateSecret = "SUPER_SECRET_SEPARATE_VALUE"
		inlineSecret   = "SUPER_SECRET_INLINE_VALUE"
		longSecret     = "SUPER_SECRET_LONG_VALUE"
		password       = "PASSWORD_MUST_NOT_LEAK"
	)
	options, err := ParseCustomArgs(
		`-o ProxyCommand=` + separateSecret +
			` -oProxyCommand=` + inlineSecret +
			` --proxy=` + longSecret +
			` -PATTACHED_VALUE --password=` + inlineSecret +
			` -P '` + password + `' -z expect`,
	)
	if err != nil {
		t.Fatalf("ParseCustomArgs() error = %v", err)
	}
	wantUnsupported := []string{"-o", "-o", "--proxy", "-P", "--password"}
	if !reflect.DeepEqual(options.Unsupported, wantUnsupported) {
		t.Fatalf("Unsupported = %#v, want %#v", options.Unsupported, wantUnsupported)
	}
	rendered := fmt.Sprint(options.Unsupported)
	for _, secret := range []string{separateSecret, inlineSecret, longSecret, password, "ProxyCommand"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("Unsupported leaked %q: %#v", secret, options.Unsupported)
		}
	}
	if options.Password != password || !options.PasswordSet {
		t.Fatalf("Password = %q, PasswordSet = %v", options.Password, options.PasswordSet)
	}
}

func TestParseCustomArgsErrorsDoNotLeakValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		wantError string
		secrets   []string
	}{
		{name: "missing password", raw: `-P`, wantError: "option -P requires a value"},
		{name: "missing mode", raw: `-z`, wantError: "option -z requires a value"},
		{name: "missing port", raw: `-p`, wantError: "option -p requires a value"},
		{name: "missing identity", raw: `-i`, wantError: "option -i requires a value"},
		{name: "invalid port", raw: `-p PORT_SECRET`, wantError: "-p requires an integer", secrets: []string{"PORT_SECRET"}},
		{name: "port zero", raw: `-p 0`, wantError: "-p requires an integer"},
		{name: "port too large", raw: `-p 65536`, wantError: "-p requires an integer"},
		{name: "invalid port after password", raw: `-P PASSWORD_SECRET -p PORT_SECRET`, wantError: "-p requires an integer", secrets: []string{"PASSWORD_SECRET", "PORT_SECRET"}},
		{name: "invalid mode after password", raw: `-P PASSWORD_SECRET -z MODE_SECRET`, wantError: "unsupported -z mode", secrets: []string{"PASSWORD_SECRET", "MODE_SECRET"}},
		{name: "unterminated password quote", raw: `-P 'QUOTE_SECRET`, wantError: "parse nccm customargs", secrets: []string{"QUOTE_SECRET"}},
		{name: "unterminated nested quote", raw: `-P "'NESTED_SECRET"`, wantError: "invalid quoting in -P value", secrets: []string{"NESTED_SECRET"}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseCustomArgs(test.raw)
			if err == nil {
				t.Fatal("ParseCustomArgs() error = nil, want error")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %q, want substring %q", err, test.wantError)
			}
			for _, secret := range test.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked secret %q: %v", secret, err)
				}
			}
		})
	}
}

func TestParseCustomArgsEmptyAndModeWithoutPassword(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "   ", "\t\n"} {
		options, err := ParseCustomArgs(raw)
		if err != nil {
			t.Fatalf("ParseCustomArgs(%q) error = %v", raw, err)
		}
		if !reflect.DeepEqual(options, CustomOptions{}) {
			t.Fatalf("ParseCustomArgs(%q) = %#v, want zero value", raw, options)
		}
	}

	options, err := ParseCustomArgs(`-z ssh`)
	if err != nil {
		t.Fatalf("ParseCustomArgs() error = %v", err)
	}
	if options.PasswordSet {
		t.Fatal("PasswordSet = true, want false")
	}
	if options.Mode != "ssh" {
		t.Fatalf("Mode = %q, want ssh", options.Mode)
	}
}
