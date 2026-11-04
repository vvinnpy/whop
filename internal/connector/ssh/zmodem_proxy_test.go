package sshconn

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"ttydeck/internal/sshconfig"
	"ttydeck/internal/zmodem"
)

type testRemote struct {
	reader *io.PipeReader
	mu     sync.Mutex
	output bytes.Buffer
}

func (r *testRemote) Read(data []byte) (int, error) { return r.reader.Read(data) }

func (r *testRemote) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.output.Write(data)
}

func (r *testRemote) written() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.output.String()
}

func TestRunZmodemAwareShellDetectsUpload(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	done := make(chan struct{})
	var uploaded []string
	options := &ZmodemOptions{
		UploadFiles: func(sshconfig.Host, io.Reader, io.Writer) ([]string, error) {
			return []string{"/tmp/local.txt"}, nil
		},
		send: func(_ context.Context, transport io.ReadWriter, files []string, _ zmodem.ProgressFunc) error {
			defer close(done)
			uploaded = append([]string(nil), files...)
			header := make([]byte, len(zmodemHeaderMarker)+2)
			if _, err := io.ReadFull(transport, header); err != nil {
				return err
			}
			if string(header) != "**\x18B01" {
				t.Fatalf("transfer prefix = %q", header)
			}
			_, err := transport.Write([]byte("protocol-reply"))
			return err
		},
		receive: nil,
	}
	var stdout, stderr bytes.Buffer
	go func() {
		_, _ = writer.Write([]byte("before\n**\x18B01payload"))
		<-done
		_ = writer.Close()
	}()
	err := runZmodemAwareShell(context.Background(), sshconfig.Host{Alias: "host"}, strings.NewReader(""), &stdout, &stderr, remote, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 1 || uploaded[0] != "/tmp/local.txt" {
		t.Fatalf("uploaded = %#v", uploaded)
	}
	if !strings.Contains(stdout.String(), "before\n") || strings.Contains(stdout.String(), "**\x18B") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(remote.written(), "protocol-reply") {
		t.Fatalf("remote writes = %q", remote.written())
	}
}

func TestRunZmodemAwareShellDetectsDownload(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	done := make(chan struct{})
	var destination string
	options := &ZmodemOptions{
		DownloadDir: func(sshconfig.Host) string { return "/tmp/downloads" },
		send:        nil,
		receive: func(_ context.Context, transport io.ReadWriter, dir string, _ int64, _ bool, _ zmodem.ProgressFunc) ([]string, error) {
			defer close(done)
			destination = dir
			header := make([]byte, len(zmodemHeaderMarker)+2)
			if _, err := io.ReadFull(transport, header); err != nil {
				return nil, err
			}
			if string(header) != "**\x18B04" {
				t.Fatalf("transfer prefix = %q", header)
			}
			return []string{"/tmp/downloads/file"}, nil
		},
	}
	go func() {
		_, _ = writer.Write([]byte("**\x18B04payload"))
		<-done
		_ = writer.Close()
	}()
	var stdout, stderr bytes.Buffer
	err := runZmodemAwareShell(context.Background(), sshconfig.Host{Alias: "host"}, strings.NewReader(""), &stdout, &stderr, remote, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if destination != "/tmp/downloads" {
		t.Fatalf("destination = %q", destination)
	}
	if stdout.Len() != 0 {
		t.Fatalf("zmodem header leaked to stdout: %q", stdout.String())
	}
}

func TestZmodemHeaderSplitAcrossReads(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	done := make(chan struct{})
	options := &ZmodemOptions{
		DownloadDir: func(sshconfig.Host) string { return "/tmp" },
		receive: func(_ context.Context, transport io.ReadWriter, _ string, _ int64, _ bool, _ zmodem.ProgressFunc) ([]string, error) {
			defer close(done)
			header := make([]byte, len(zmodemHeaderMarker)+2)
			_, err := io.ReadFull(transport, header)
			if err != nil {
				return nil, err
			}
			if string(header) != "**\x18B04" {
				t.Fatalf("prefix = %q", header)
			}
			return nil, nil
		},
	}
	go func() {
		for _, part := range []string{"prompt", "**\x18", "B04rest"} {
			_, _ = writer.Write([]byte(part))
		}
		<-done
		_ = writer.Close()
	}()
	var stdout, stderr bytes.Buffer
	if err := runZmodemAwareShell(context.Background(), sshconfig.Host{}, strings.NewReader(""), &stdout, &stderr, remote, nil, options); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "prompt" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
