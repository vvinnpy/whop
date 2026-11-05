package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzLoadDoesNotPanic(f *testing.F) {
	seeds := [][]byte{
		[]byte("Host prod\n  HostName 10.0.0.1\n  User deploy\n  Port 22\n"),
		[]byte("Include missing/*.conf\nHost *\n  User git\n"),
		[]byte("Host quoted \"bad\n  Port nope\n"),
		[]byte("Host v6\n  HostName [2001:db8::1]\n  ProxyJump bastion\n"),
		[]byte("Host \xff\xfe\n"),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	dir := f.TempDir()
	path := filepath.Join(dir, "config")
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			t.Skip()
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = Load(path)
	})
}
