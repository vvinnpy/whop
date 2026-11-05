package zmodem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	zm "github.com/xx25/go-zmodem"
)

type chanReader struct {
	ch  chan []byte
	buf []byte
}

func (cr *chanReader) Read(p []byte) (int, error) {
	if len(cr.buf) > 0 {
		n := copy(p, cr.buf)
		cr.buf = cr.buf[n:]
		return n, nil
	}
	data, ok := <-cr.ch
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, data)
	if n < len(data) {
		cr.buf = data[n:]
	}
	return n, nil
}

type chanWriter struct {
	ch chan []byte
}

func (cw *chanWriter) Write(p []byte) (int, error) {
	buf := make([]byte, len(p))
	copy(buf, p)
	cw.ch <- buf
	return len(p), nil
}

func (cw *chanWriter) Close() error {
	close(cw.ch)
	return nil
}

func bufferedPipe(size int) (*chanReader, *chanWriter) {
	ch := make(chan []byte, size)
	return &chanReader{ch: ch}, &chanWriter{ch: ch}
}

type pipeReadWriter struct {
	io.Reader
	io.Writer
}

func newBufferedTransports() (sender, receiver io.ReadWriter, senderClose, receiverClose func()) {
	r1, w1 := bufferedPipe(256)
	r2, w2 := bufferedPipe(256)
	sender = &pipeReadWriter{Reader: r2, Writer: w1}
	receiver = &pipeReadWriter{Reader: r1, Writer: w2}
	return sender, receiver, func() { _ = w1.Close() }, func() { _ = w2.Close() }
}
func TestSendReceiveLoopback(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	content := "hello zmodem\nsecond line\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest")

	senderConn, receiverConn, senderClose, receiverClose := newBufferedTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		wg         sync.WaitGroup
		sendErr    error
		receiveErr error
		received   []string
		progress   []Progress
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		sendErr = Send(ctx, senderConn, []string{source}, func(event Progress) {
			progress = append(progress, event)
		})
		senderClose()
	}()
	go func() {
		defer wg.Done()
		received, receiveErr = Receive(ctx, receiverConn, dest, 0, false, nil)
		receiverClose()
	}()
	wg.Wait()

	if sendErr != nil {
		t.Fatalf("send: %v", sendErr)
	}
	if receiveErr != nil {
		t.Fatalf("receive: %v", receiveErr)
	}
	if len(received) != 1 {
		t.Fatalf("received = %#v", received)
	}
	got, err := os.ReadFile(received[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("content = %q", got)
	}
	if len(progress) == 0 || !progress[len(progress)-1].Done {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestReceiveSkipsExistingWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("new content"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dest, "source.txt")
	if err := os.WriteFile(existing, []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}

	senderConn, receiverConn, senderClose, receiverClose := newBufferedTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var sendErr, receiveErr error
	var received []string
	wg.Add(2)
	go func() {
		defer wg.Done()
		sendErr = Send(ctx, senderConn, []string{source}, nil)
		senderClose()
	}()
	go func() {
		defer wg.Done()
		received, receiveErr = Receive(ctx, receiverConn, dest, 0, false, nil)
		receiverClose()
	}()
	wg.Wait()
	if sendErr != nil || receiveErr != nil {
		t.Fatalf("send=%v receive=%v", sendErr, receiveErr)
	}
	if len(received) != 0 {
		t.Fatalf("expected skipped file, got %#v", received)
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old content" {
		t.Fatalf("existing file overwritten: %q", got)
	}
}

func TestSafeJoinSanitizesTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../evil", "../../etc/passwd", "..\\evil"} {
		path, err := safeJoin(dir, name)
		if err != nil {
			t.Fatalf("safeJoin(%q): %v", name, err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("unsafe path for %q: %q", name, path)
		}
	}
	path, err := safeJoin(dir, "normal.txt")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || filepath.Base(path) != "normal.txt" {
		t.Fatalf("path = %q", path)
	}
}

func TestWaitForHeader(t *testing.T) {
	for _, data := range []string{"prefix **\x18B00", "prefix *\x18A01", "prefix *\x18C04", "prefix *\x18D0a"} {
		if !WaitForHeader([]byte(data)) {
			t.Fatalf("expected header in %q", data)
		}
	}
	if WaitForHeader([]byte("no header")) {
		t.Fatal("unexpected header")
	}
}

func TestSendRequiresFiles(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if err := Send(context.Background(), c1, nil, nil); err == nil || !strings.Contains(err.Error(), "no files") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReceiverFailureRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	r := &receiver{destDir: dir, overwrite: true}
	info := zm.FileInfo{Name: "partial.bin", Size: 10}
	writer, _, err := r.AcceptFile(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	r.FileCompleted(info, 7, errors.New("transfer failed"))
	if _, err := os.Stat(filepath.Join(dir, "partial.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final file exists after failure: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestReceiverOverwriteKeepsExistingFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep.bin")
	if err := os.WriteFile(target, []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &receiver{destDir: dir, overwrite: true}
	info := zm.FileInfo{Name: "keep.bin", Size: 10}
	writer, _, err := r.AcceptFile(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("new partial")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	r.FileCompleted(info, 11, errors.New("transfer failed"))
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old content" {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestReceiverAtomicallyReplacesExistingFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "replace.bin")
	if err := os.WriteFile(target, []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &receiver{destDir: dir, overwrite: true}
	info := zm.FileInfo{Name: "replace.bin", Size: 11}
	writer, _, err := r.AcceptFile(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("new content")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	r.FileCompleted(info, 11, nil)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new content" {
		t.Fatalf("replace failed: %q", got)
	}
	if len(r.completed) != 1 || r.completed[0] != target {
		t.Fatalf("completed = %#v", r.completed)
	}
}

func TestReceiverCleanupRemovesPendingTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	r := &receiver{destDir: dir}
	if _, _, err := r.AcceptFile(zm.FileInfo{Name: "pending.bin", Size: 10}); err != nil {
		t.Fatal(err)
	}
	r.cleanup()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestReceiverDoesNotOverwriteFileCreatedDuringTransfer(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "race.bin")
	r := &receiver{destDir: dir}
	info := zm.FileInfo{Name: "race.bin", Size: 10}
	writer, _, err := r.AcceptFile(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("other writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("our data")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	r.FileCompleted(info, 8, nil)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "other writer" {
		t.Fatalf("file created during transfer was overwritten: %q", got)
	}
	if len(r.completed) != 0 {
		t.Fatalf("completed = %#v", r.completed)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "race.bin" {
		t.Fatalf("unexpected files: %v", entries)
	}
}

func TestProgressPrinterUsesSingleLine(t *testing.T) {
	var out bytes.Buffer
	progress := progressPrinter(&out, true)
	progress(Progress{Name: "file.bin", Transferred: 1024, Total: 4096})
	progress(Progress{Name: "file.bin", Transferred: 2048, Total: 4096})
	progress(Progress{Name: "file.bin", Transferred: 4096, Total: 4096, Done: true})
	got := out.String()
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("progress output has extra lines: %q", got)
	}
	if !strings.Contains(got, "file.bin: done (4096 bytes)") {
		t.Fatalf("progress output = %q", got)
	}
}

func TestProgressPrinterErrorEndsLine(t *testing.T) {
	var out bytes.Buffer
	progressPrinter(&out, true)(Progress{Name: "file.bin", Done: true, Err: errors.New("connection lost")})
	got := out.String()
	if !strings.HasPrefix(got, "\rfile.bin: connection lost") || !strings.HasSuffix(got, "\r\n") {
		t.Fatalf("progress error = %q", got)
	}
}

func TestProgressPrinterNonTerminalOnlyPrintsFinalStatus(t *testing.T) {
	var out bytes.Buffer
	progress := progressPrinter(&out, false)
	progress(Progress{Name: "file.bin", Transferred: 1024, Total: 4096})
	progress(Progress{Name: "file.bin", Transferred: 4096, Total: 4096, Done: true})
	if strings.Contains(out.String(), "\r") {
		t.Fatalf("non-terminal progress contains carriage returns: %q", out.String())
	}
	if out.String() != "file.bin: done (4096 bytes)\n" {
		t.Fatalf("non-terminal progress = %q", out.String())
	}
}

func TestSanitizeProgressTextPreservesChinese(t *testing.T) {
	got := sanitizeProgressText("中文\x1b[31m文件\n名称")
	if !strings.Contains(got, "中文") || !strings.Contains(got, "文件") || !strings.Contains(got, "名称") {
		t.Fatalf("sanitized text = %q", got)
	}
	if strings.ContainsAny(got, "\x1b\n\r") {
		t.Fatalf("control characters leaked: %q", got)
	}
}

func TestProgressPrinterWideNamePadding(t *testing.T) {
	var out bytes.Buffer
	progress := progressPrinter(&out, true)
	progress(Progress{Name: "中文文件.bin", Transferred: 123456789, Total: 123456789})
	progress(Progress{Name: "中文文件.bin", Transferred: 123456789, Total: 123456789, Done: true})
	got := strings.TrimSuffix(out.String(), "\r\n")
	parts := strings.Split(got, "\r")
	if len(parts) < 2 {
		t.Fatalf("progress output = %q", out.String())
	}
	progressLine := parts[len(parts)-2]
	doneLine := parts[len(parts)-1]
	if ansi.StringWidth(doneLine) < ansi.StringWidth(progressLine) {
		t.Fatalf("done line width %d < progress width %d: %q", ansi.StringWidth(doneLine), ansi.StringWidth(progressLine), out.String())
	}
}
