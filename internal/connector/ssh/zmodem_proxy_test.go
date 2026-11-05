package sshconn

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whop/internal/sshconfig"
	"whop/internal/zmodem"
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
			header := make([]byte, len("**\x18B")+2)
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
			header := make([]byte, len("**\x18B")+2)
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
			header := make([]byte, len("**\x18B")+2)
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

type notifyWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	writes chan string
}

func newNotifyWriter() *notifyWriter {
	return &notifyWriter{writes: make(chan string, 16)}
}

func (w *notifyWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buffer.Write(data)
	chunk := string(append([]byte(nil), data...))
	w.mu.Unlock()
	select {
	case w.writes <- chunk:
	default:
	}
	return n, err
}

func (w *notifyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestRunZmodemAwareShellFlushesPromptImmediately(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	out := newNotifyWriter()
	done := make(chan error, 1)
	go func() {
		done <- runZmodemAwareShell(context.Background(), sshconfig.Host{}, strings.NewReader(""), out, io.Discard, remote, nil, &ZmodemOptions{})
	}()

	if _, err := writer.Write([]byte("]# ")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-out.writes:
		if got != "]# " {
			t.Fatalf("prompt chunk = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt ending in ordinary characters was buffered")
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunZmodemAwareShellFlushesMarkerPrefixAtEOF(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	var stdout, stderr bytes.Buffer
	go func() {
		_, _ = writer.Write([]byte("*"))
		_ = writer.Close()
	}()
	if err := runZmodemAwareShell(context.Background(), sshconfig.Host{}, strings.NewReader(""), &stdout, &stderr, remote, nil, &ZmodemOptions{}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "*" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestFindZmodemHeaderHexAndBinary(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		start int
		typ   byte
		hex   bool
	}{
		{name: "hex ZRQINIT", data: []byte("prefix**\x18B00rest"), start: 6, typ: 0x00, hex: true},
		{name: "binary ZFILE", data: []byte("*\x18C\x04rest"), start: 0, typ: 0x04},
		{name: "binary ZRINIT", data: []byte("*\x18A\x01rest"), start: 0, typ: 0x01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header, found, needMore := findZmodemHeader(tt.data)
			if !found || needMore {
				t.Fatalf("found=%v needMore=%v", found, needMore)
			}
			if header.start != tt.start || header.typ != tt.typ || header.hex != tt.hex {
				t.Fatalf("header = %#v", header)
			}
		})
	}
}

func TestFindZmodemHeaderWaitsForFrameType(t *testing.T) {
	header, found, needMore := findZmodemHeader([]byte("**\x18B0"))
	if found || !needMore {
		t.Fatalf("found=%v needMore=%v", found, needMore)
	}
	if header.start != 0 {
		t.Fatalf("start = %d", header.start)
	}
}

func TestZmodemPreviewFlushLen(t *testing.T) {
	tests := []struct {
		pending string
		want    int
	}{
		{pending: "plain text", want: 10},
		{pending: "plain*", want: 5},
		{pending: "plain**", want: 5},
		{pending: "plain**\x18", want: 5},
		{pending: "plain*\x18", want: 5},
		{pending: "plain\x18", want: 6},
	}
	for _, tt := range tests {
		if got := zmodemPreviewFlushLen([]byte(tt.pending)); got != tt.want {
			t.Errorf("zmodemPreviewFlushLen(%q) = %d, want %d", tt.pending, got, tt.want)
		}
	}
}

func TestRunZmodemAwareShellDetectsBinaryHeader(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	done := make(chan struct{})
	options := &ZmodemOptions{
		DownloadDir: func(sshconfig.Host) string { return "/tmp/downloads" },
		receive: func(_ context.Context, transport io.ReadWriter, _ string, _ int64, _ bool, _ zmodem.ProgressFunc) ([]string, error) {
			defer close(done)
			header := make([]byte, len("*\x18C\x04"))
			if _, err := io.ReadFull(transport, header); err != nil {
				return nil, err
			}
			if string(header) != "*\x18C\x04" {
				t.Fatalf("prefix = %q", header)
			}
			return nil, nil
		},
	}
	go func() {
		_, _ = writer.Write([]byte("*\x18C\x04payload"))
		<-done
		_ = writer.Close()
	}()
	var stdout, stderr bytes.Buffer
	if err := runZmodemAwareShell(context.Background(), sshconfig.Host{Alias: "host"}, strings.NewReader(""), &stdout, &stderr, remote, nil, options); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunZmodemAwareShellDebugLogsHeader(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	done := make(chan struct{})
	var debug bytes.Buffer
	options := &ZmodemOptions{
		DownloadDir: func(sshconfig.Host) string { return "/tmp/downloads" },
		Debug:       &debug,
		receive: func(_ context.Context, transport io.ReadWriter, _ string, _ int64, _ bool, _ zmodem.ProgressFunc) ([]string, error) {
			defer close(done)
			header := make([]byte, len("**\x18B00"))
			_, err := io.ReadFull(transport, header)
			return nil, err
		},
	}
	go func() {
		_, _ = writer.Write([]byte("**\x18B00payload"))
		<-done
		_ = writer.Close()
	}()
	var stdout, stderr bytes.Buffer
	if err := runZmodemAwareShell(context.Background(), sshconfig.Host{Alias: "host"}, strings.NewReader(""), &stdout, &stderr, remote, nil, options); err != nil {
		t.Fatal(err)
	}
	want := "detected hex header type=0x00 (ZRQINIT)"
	if !strings.Contains(debug.String(), want) || !strings.Contains(debug.String(), "sample=") {
		t.Fatalf("debug log = %q", debug.String())
	}
}

type deadlineWriter struct {
	mu       sync.Mutex
	deadline time.Time
}

func (w *deadlineWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *deadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	return nil
}

func TestRouteLocalInputCtrlCCancelsTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var transferCancel atomic.Pointer[context.CancelFunc]
	transferCancel.Store(&cancel)
	var requested atomic.Bool
	paused := atomic.Bool{}
	paused.Store(true)
	input := make(chan []byte, 1)
	input <- []byte{0x03}
	close(input)
	deadline := &deadlineWriter{}
	writer := &serializedRemoteWriter{writer: deadline, paused: &paused, mu: &sync.Mutex{}}
	routeLocalInput(input, writer, &paused, &promptGate{}, &transferCancel, &requested)
	if ctx.Err() != context.Canceled {
		t.Fatalf("context error = %v", ctx.Err())
	}
	if !requested.Load() {
		t.Fatal("cancel was not marked as requested")
	}
	deadline.mu.Lock()
	defer deadline.mu.Unlock()
	if deadline.deadline.IsZero() {
		t.Fatal("read deadline was not set to unblock the transfer")
	}
}

func TestRunZmodemAwareShellDetectsZeroCRCHeader(t *testing.T) {
	reader, writer := io.Pipe()
	remote := &testRemote{reader: reader}
	done := make(chan struct{})
	var debug bytes.Buffer
	options := &ZmodemOptions{
		DownloadDir: func(sshconfig.Host) string { return "/tmp/downloads" },
		Debug:       &debug,
		receive: func(_ context.Context, transport io.ReadWriter, _ string, _ int64, _ bool, _ zmodem.ProgressFunc) ([]string, error) {
			defer close(done)
			header := make([]byte, len("**\x18B00"))
			_, err := io.ReadFull(transport, header)
			return nil, err
		},
	}
	payload := "rz\r**\x18B" + strings.Repeat("0", 14) + "\r\nwaiting for cancel\r\n"
	go func() {
		_, _ = writer.Write([]byte(payload))
		<-done
		_ = writer.Close()
	}()
	var stdout, stderr bytes.Buffer
	if err := runZmodemAwareShell(context.Background(), sshconfig.Host{Alias: "host"}, strings.NewReader(""), &stdout, &stderr, remote, nil, options); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "rz\r" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(debug.String(), "ZRQINIT") {
		t.Fatalf("debug = %q", debug.String())
	}
}
