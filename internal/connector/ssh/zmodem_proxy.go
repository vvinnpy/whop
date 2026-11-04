package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"whop/internal/sshconfig"
	"whop/internal/zmodem"
)

var zmodemHeaderMarker = []byte("**\x18B")

// ZmodemOptions configures automatic ZMODEM interception during an interactive
// SSH shell. UploadFiles is called when the remote runs rz; it should return
// local files or an empty list to cancel. DownloadDir selects the destination
// for files sent by remote sz.
type ZmodemOptions struct {
	UploadFiles func(host sshconfig.Host, stdin io.Reader, stderr io.Writer) ([]string, error)
	DownloadDir func(host sshconfig.Host) string
	MaxFileSize int64
	Overwrite   bool
	Progress    io.Writer

	send    func(context.Context, io.ReadWriter, []string, zmodem.ProgressFunc) error
	receive func(context.Context, io.ReadWriter, string, int64, bool, zmodem.ProgressFunc) ([]string, error)
}

func (o *ZmodemOptions) withDefaults() *ZmodemOptions {
	if o == nil {
		return nil
	}
	copy := *o
	if copy.UploadFiles == nil {
		copy.UploadFiles = func(sshconfig.Host, io.Reader, io.Writer) ([]string, error) {
			return nil, errors.New("zmodem: upload prompt is unavailable")
		}
	}
	if copy.DownloadDir == nil {
		copy.DownloadDir = func(sshconfig.Host) string { return "." }
	}
	if copy.Progress == nil {
		copy.Progress = io.Discard
	}
	if copy.send == nil {
		copy.send = zmodem.Send
	}
	if copy.receive == nil {
		copy.receive = zmodem.Receive
	}
	return &copy
}

func runZmodemAwareShell(ctx context.Context, host sshconfig.Host, stdin io.Reader, stdout, stderr io.Writer, remote io.ReadWriter, terminal *terminalMode, options *ZmodemOptions) error {
	options = options.withDefaults()
	if options == nil {
		return errors.New("zmodem: missing options")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	var paused atomic.Bool
	writer := &serializedRemoteWriter{writer: remote, paused: &paused, mu: &sync.Mutex{}}
	inputCh := make(chan []byte, 64)
	gate := &promptGate{}
	go readLocalInput(stdin, inputCh)
	go routeLocalInput(inputCh, writer, &paused, gate)

	pending := make([]byte, 0, 32*1024)
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := remote.Read(buffer)
		if n > 0 {
			pending = append(pending, buffer[:n]...)
			for {
				index := bytes.Index(pending, zmodemHeaderMarker)
				if index < 0 {
					flush := zmodemPreviewFlushLen(pending, zmodemHeaderMarker)
					if flush > 0 {
						if _, err := stdout.Write(pending[:flush]); err != nil {
							return err
						}
						pending = append(pending[:0], pending[flush:]...)
					}
					break
				}
				if len(pending) < index+len(zmodemHeaderMarker)+2 {
					// Wait for the ZMODEM frame type bytes to arrive.
					break
				}
				if index > 0 {
					if _, err := stdout.Write(pending[:index]); err != nil {
						return err
					}
				}
				header := append([]byte(nil), pending[index:]...)
				pending = pending[:0]
				paused.Store(true)
				var transferErr error
				if header[len(zmodemHeaderMarker)+1] == '1' {
					transferErr = handleZmodemUpload(ctx, host, stderr, remote, writer, terminal, gate, options, header)
				} else {
					transferErr = handleZmodemDownload(ctx, host, stderr, remote, writer, terminal, options, header)
				}
				paused.Store(false)
				_ = writer.writeProtocol([]byte("\n"))
				if transferErr != nil {
					fmt.Fprintf(stderr, "\r\nwhop zmodem: %v\r\n", transferErr)
					_ = writer.writeProtocol(zmodemCancel)
				} else {
					fmt.Fprint(stderr, "\r\nwhop zmodem: transfer complete\r\n")
				}
				break
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if len(pending) > 0 {
					if _, err := stdout.Write(pending); err != nil {
						return err
					}
				}
				return nil
			}
			return readErr
		}
	}
}

// zmodemPreviewFlushLen returns how many bytes can be emitted immediately
// without losing a split ZMODEM header. Ordinary terminal output must never be
// delayed by the marker length; only a suffix that is actually a marker prefix
// needs to remain buffered.
func zmodemPreviewFlushLen(pending, marker []byte) int {
	maxHold := len(marker) - 1
	if len(pending) < maxHold {
		maxHold = len(pending)
	}
	for hold := maxHold; hold > 0; hold-- {
		if bytes.Equal(pending[len(pending)-hold:], marker[:hold]) {
			return len(pending) - hold
		}
	}
	return len(pending)
}

func readLocalInput(stdin io.Reader, output chan<- []byte) {
	if stdin == nil {
		close(output)
		return
	}
	buffer := make([]byte, 32*1024)
	for {
		n, err := stdin.Read(buffer)
		if n > 0 {
			data := append([]byte(nil), buffer[:n]...)
			output <- data
		}
		if err != nil {
			close(output)
			return
		}
	}
}

func routeLocalInput(input <-chan []byte, writer *serializedRemoteWriter, paused *atomic.Bool, gate *promptGate) {
	for data := range input {
		if gate.write(data) {
			continue
		}
		if paused.Load() {
			continue
		}
		if err := writer.writeInteractive(data); err != nil {
			return
		}
	}
}

func handleZmodemUpload(ctx context.Context, host sshconfig.Host, stderr io.Writer, remote io.Reader, writer *serializedRemoteWriter, terminal *terminalMode, gate *promptGate, options *ZmodemOptions, header []byte) error {
	promptInput := gate.begin()
	if terminal != nil {
		if err := terminal.Suspend(); err != nil {
			gate.end(promptInput)
			return fmt.Errorf("restore terminal for file prompt: %w", err)
		}
	}
	files, err := options.UploadFiles(host, promptInput, stderr)
	if terminal != nil {
		if resumeErr := terminal.Resume(); resumeErr != nil && err == nil {
			err = fmt.Errorf("restore raw terminal: %w", resumeErr)
		}
	}
	gate.end(promptInput)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("upload cancelled")
	}
	transport := &zmodemTransport{
		Reader: &prefixReader{prefix: header, reader: remote},
		writer: writer,
	}
	return options.send(ctx, transport, files, zmodem.ProgressPrinter(options.Progress))
}

func handleZmodemDownload(ctx context.Context, host sshconfig.Host, stderr io.Writer, remote io.Reader, writer *serializedRemoteWriter, terminal *terminalMode, options *ZmodemOptions, header []byte) error {
	dir := options.DownloadDir(host)
	if dir == "" {
		return errors.New("download directory is empty")
	}
	transport := &zmodemTransport{
		Reader: &prefixReader{prefix: header, reader: remote},
		writer: writer,
	}
	_, err := options.receive(ctx, transport, dir, options.MaxFileSize, options.Overwrite, zmodem.ProgressPrinter(options.Progress))
	return err
}

type promptGate struct {
	mu     sync.RWMutex
	writer *io.PipeWriter
}

func (g *promptGate) begin() *io.PipeReader {
	reader, writer := io.Pipe()
	g.mu.Lock()
	g.writer = writer
	g.mu.Unlock()
	return reader
}

func (g *promptGate) end(reader *io.PipeReader) {
	g.mu.Lock()
	writer := g.writer
	g.writer = nil
	g.mu.Unlock()
	if writer != nil {
		_ = writer.Close()
	}
	_ = reader.Close()
}

func (g *promptGate) write(data []byte) bool {
	g.mu.RLock()
	writer := g.writer
	g.mu.RUnlock()
	if writer == nil {
		return false
	}
	_, _ = writer.Write(data)
	return true
}

type serializedRemoteWriter struct {
	mu     *sync.Mutex
	writer io.Writer
	paused *atomic.Bool
}

func (w *serializedRemoteWriter) writeInteractive(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.paused.Load() {
		return nil
	}
	return writeAll(w.writer, data)
}

func (w *serializedRemoteWriter) writeProtocol(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeAll(w.writer, data)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

type prefixReader struct {
	prefix []byte
	reader io.Reader
}

func (r *prefixReader) Read(data []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(data, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return r.reader.Read(data)
}

type zmodemTransport struct {
	io.Reader
	writer *serializedRemoteWriter
}

func (t *zmodemTransport) Write(data []byte) (int, error) {
	if err := t.writer.writeProtocol(data); err != nil {
		return 0, err
	}
	return len(data), nil
}

var zmodemCancel = []byte{0x18, 0x18, 0x18, 0x18, 0x18}
