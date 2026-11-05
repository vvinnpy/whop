package sshconn

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"whop/internal/sshconfig"
)

func TestFileTransferReadWriteAndNavigate(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	host := sshconfig.Host{
		Alias:         "sftp-target",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	ctx := context.Background()
	transfer, err := newTestConnector().OpenFileTransfer(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	defer transfer.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	entries, err := transfer.ReadDir(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" || entries[0].Directory {
		t.Fatalf("entries = %#v", entries)
	}
	remote, err := transfer.Open(ctx, entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(remote)
	_ = remote.Close()
	if err != nil || string(data) != "hello" {
		t.Fatalf("read = %q, err=%v", data, err)
	}

	nested := "nested"
	if err := transfer.Mkdir(ctx, nested); err != nil {
		t.Fatal(err)
	}
	uploadPath := nested + "/upload.txt"
	writer, err := transfer.Create(ctx, uploadPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "uploaded"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	stat, err := transfer.Stat(ctx, uploadPath)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size != 8 || stat.Directory {
		t.Fatalf("stat = %#v", stat)
	}
	renamed := nested + "/renamed.txt"
	if err := transfer.Rename(ctx, uploadPath, renamed); err != nil {
		t.Fatal(err)
	}
	if err := transfer.Remove(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	if err := transfer.RemoveAll(ctx, nested); err != nil {
		t.Fatal(err)
	}
}

func TestFileTransferHonorsCanceledContext(t *testing.T) {
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	host := sshconfig.Host{
		Alias:         "sftp-target",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	transfer, err := newTestConnector().OpenFileTransfer(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer transfer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transfer.ReadDir(ctx, "."); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("ReadDir error = %v", err)
	}
	if _, err := transfer.Open(ctx, "file"); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("Open error = %v", err)
	}
}
