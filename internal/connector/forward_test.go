package connector

import (
	"reflect"
	"testing"
)

func TestParseForwardSpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind ForwardKind
		raw  string
		want ForwardSpec
	}{
		{name: "local bind", kind: ForwardLocal, raw: "127.0.0.1:8080:db.internal:5432", want: ForwardSpec{Kind: ForwardLocal, BindHost: "127.0.0.1", BindPort: 8080, DestinationHost: "db.internal", DestinationPort: 5432}},
		{name: "local default bind", kind: ForwardLocal, raw: "8080:db.internal:5432", want: ForwardSpec{Kind: ForwardLocal, BindHost: "127.0.0.1", BindPort: 8080, DestinationHost: "db.internal", DestinationPort: 5432}},
		{name: "local ipv6 bind and destination", kind: ForwardLocal, raw: "[::1]:8080:[2001:db8::1]:5432", want: ForwardSpec{Kind: ForwardLocal, BindHost: "::1", BindPort: 8080, DestinationHost: "2001:db8::1", DestinationPort: 5432}},
		{name: "remote wildcard", kind: ForwardRemote, raw: "0.0.0.0:9000:localhost:3000", want: ForwardSpec{Kind: ForwardRemote, BindHost: "0.0.0.0", BindPort: 9000, DestinationHost: "localhost", DestinationPort: 3000}},
		{name: "dynamic port", kind: ForwardDynamic, raw: "1080", want: ForwardSpec{Kind: ForwardDynamic, BindHost: "127.0.0.1", BindPort: 1080}},
		{name: "dynamic bind", kind: ForwardDynamic, raw: "127.0.0.1:1080", want: ForwardSpec{Kind: ForwardDynamic, BindHost: "127.0.0.1", BindPort: 1080}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseForwardSpec(test.kind, test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseForwardSpecRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind ForwardKind
		raw  string
	}{
		{kind: ForwardLocal, raw: ""},
		{kind: ForwardLocal, raw: "8080:db"},
		{kind: ForwardLocal, raw: "8080:db:0"},
		{kind: ForwardLocal, raw: "70000:db:22"},
		{kind: ForwardRemote, raw: "8080::22"},
		{kind: ForwardDynamic, raw: "127.0.0.1:notaport"},
		{kind: ForwardDynamic, raw: "1080:db:22"},
		{kind: "socks4", raw: "1080"},
	}
	for _, test := range tests {
		if _, err := ParseForwardSpec(test.kind, test.raw); err == nil {
			t.Fatalf("ParseForwardSpec(%q, %q) unexpectedly succeeded", test.kind, test.raw)
		}
	}
}
