package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"whop/internal/sshconfig"
	"whop/internal/zmodem"
)

var zmodemHeaderPrefixes = [][]byte{
	[]byte("**\x18B"),
	[]byte("*\x18A"),
	[]byte("*\x18C"),
	[]byte("*\x18D"),
}

type zmodemHeader struct {
	start int
	typ   byte
	hex   bool
}

type readDeadlineSetter interface {
	SetReadDeadline(time.Time) error
}

// ZmodemOptions configures automatic ZMODEM interception during an interactive
// SSH shell. UploadFiles is called when the remote runs rz; it should return
// local files or an empty list to cancel. DownloadDir selects the destination
// for files sent by remote sz. Debug is optional and should be opt-in.
type ZmodemOptions struct {
	UploadFiles func(host sshconfig.Host, stdin io.Reader, stderr io.Writer) ([]string, error)
	DownloadDir func(host sshconfig.Host) string
	MaxFileSize int64
	Overwrite   bool
	Progress    io.Writer
	Debug       io.Writer

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
	var cancelRequested atomic.Bool
	var transferCancel atomic.Pointer[context.CancelFunc]
	writer := &serializedRemoteWriter{writer: remote, paused: &paused, mu: &sync.Mutex{}}
	inputCh := make(chan []byte, 64)
	gate := &promptGate{}
	go readLocalInput(stdin, inputCh)
	go routeLocalInput(inputCh, writer, &paused, gate, &transferCancel, &cancelRequested)

	pending := make([]byte, 0, 32*1024)
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := remote.Read(buffer)
		if n > 0 {
			pending = append(pending, buffer[:n]...)
			for {
				headerInfo, found, needMore := findZmodemHeader(pending)
				if !found {
					if needMore {
						if headerInfo.start > 0 {
							if _, err := stdout.Write(pending[:headerInfo.start]); err != nil {
								return err
							}
							pending = append(pending[:0], pending[headerInfo.start:]...)
						}
						break
					}
					flush := zmodemPreviewFlushLen(pending)
					if flush > 0 {
						if _, err := stdout.Write(pending[:flush]); err != nil {
							return err
						}
						pending = append(pending[:0], pending[flush:]...)
					}
					break
				}
				if headerInfo.start > 0 {
					if _, err := stdout.Write(pending[:headerInfo.start]); err != nil {
						return err
					}
				}
				header := append([]byte(nil), pending[headerInfo.start:]...)
				pending = pending[:0]
				if options.Debug != nil {
					logZmodemHeader(options.Debug, headerInfo, header)
				}

				cancelRequested.Store(false)
				transferCtx, cancel := context.WithCancel(ctx)
				transferCancel.Store(&cancel)
				paused.Store(true)
				var transferErr error
				if headerInfo.typ == 0x01 { // ZRINIT: remote is waiting to upload.
					transferErr = handleZmodemUpload(transferCtx, host, stderr, remote, writer, terminal, gate, options, header)
				} else {
					transferErr = handleZmodemDownload(transferCtx, host, remote, writer, options, header)
				}
				cancel()
				transferCancel.Store(nil)
				if setter, ok := remote.(readDeadlineSetter); ok {
					_ = setter.SetReadDeadline(time.Time{})
				}
				if transferErr != nil {
					if ctx.Err() != nil {
						transferErr = ctx.Err()
					}
					fmt.Fprintf(stderr, "\r\nwhop zmodem: %v\r\n", transferErr)
					_ = writer.writeProtocol(zmodemCancel)
					// A broken sender can leave ZRINIT bytes in the pty input
					// queue. Closing the session is the only reliable way to keep
					// the shell from executing that binary text as a command.
					return fmt.Errorf("zmodem transfer aborted: %w", transferErr)
				}
				paused.Store(false)
				fmt.Fprint(stderr, "\r\nwhop zmodem: transfer complete\r\n")
				break
			}
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
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

// findZmodemHeader locates a complete hexadecimal or binary ZMODEM header.
// needMore reports that a header prefix was found but its frame-type bytes have
// not arrived yet.
func findZmodemHeader(data []byte) (zmodemHeader, bool, bool) {
	for i := 0; i+2 < len(data); i++ {
		if data[i] != '*' || data[i+1] != 0x18 {
			continue
		}
		encoding := data[i+2]
		start := i
		hex := false
		switch encoding {
		case 'A', 'C', 'D':
		case 'B':
			if i == 0 || data[i-1] != '*' {
				continue
			}
			start = i - 1
			hex = true
		default:
			continue
		}

		typePos := i + 3
		if hex {
			if len(data) < typePos+2 {
				return zmodemHeader{start: start, hex: true}, false, true
			}
			typ, ok := parseHexByte(data[typePos], data[typePos+1])
			if !ok {
				continue
			}
			return zmodemHeader{start: start, typ: typ, hex: true}, true, false
		}
		if len(data) < typePos+1 {
			return zmodemHeader{start: start}, false, true
		}
		return zmodemHeader{start: start, typ: data[typePos]}, true, false
	}
	return zmodemHeader{}, false, false
}

func parseHexByte(high, low byte) (byte, bool) {
	h, ok := hexNibble(high)
	if !ok {
		return 0, false
	}
	l, ok := hexNibble(low)
	if !ok {
		return 0, false
	}
	return h<<4 | l, true
}

func hexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

// zmodemPreviewFlushLen returns how many bytes can be emitted immediately
// without losing a split ZMODEM header. Ordinary terminal output must never be
// delayed by the marker length; only a suffix that is actually a header prefix
// needs to remain buffered.
func zmodemPreviewFlushLen(pending []byte) int {
	maxHold := 0
	for _, prefix := range zmodemHeaderPrefixes {
		if len(prefix) > maxHold {
			maxHold = len(prefix)
		}
	}
	maxHold--
	if len(pending) < maxHold {
		maxHold = len(pending)
	}
	for hold := maxHold; hold > 0; hold-- {
		suffix := pending[len(pending)-hold:]
		for _, prefix := range zmodemHeaderPrefixes {
			if hold < len(prefix) && bytes.Equal(suffix, prefix[:hold]) {
				return len(pending) - hold
			}
		}
	}
	return len(pending)
}

func logZmodemHeader(out io.Writer, header zmodemHeader, data []byte) {
	sample := data
	if len(sample) > 64 {
		sample = sample[:64]
	}
	encoding := "binary"
	if header.hex {
		encoding = "hex"
	}
	fmt.Fprintf(out, "\r\nwhop zmodem: detected %s header type=0x%02x (%s) sample=% x\r\n",
		encoding, header.typ, zmodemFrameTypeName(header.typ), sample)
}

func zmodemFrameTypeName(typ byte) string {
	switch typ {
	case 0x00:
		return "ZRQINIT"
	case 0x01:
		return "ZRINIT"
	case 0x02:
		return "ZSINIT"
	case 0x03:
		return "ZACK"
	case 0x04:
		return "ZFILE"
	case 0x05:
		return "ZSKIP"
	case 0x06:
		return "ZNAK"
	case 0x07:
		return "ZABORT"
	case 0x08:
		return "ZFIN"
	case 0x09:
		return "ZRPOS"
	case 0x0a:
		return "ZDATA"
	case 0x0b:
		return "ZEOF"
	default:
		return "unknown"
	}
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

func routeLocalInput(input <-chan []byte, writer *serializedRemoteWriter, paused *atomic.Bool, gate *promptGate, transferCancel *atomic.Pointer[context.CancelFunc], cancelRequested *atomic.Bool) {
	for data := range input {
		if gate.write(data) {
			continue
		}
		if paused.Load() {
			if bytes.IndexByte(data, 0x03) >= 0 && cancelRequested.CompareAndSwap(false, true) {
				if cancel := transferCancel.Load(); cancel != nil {
					(*cancel)()
				}
				if setter, ok := writer.writer.(readDeadlineSetter); ok {
					_ = setter.SetReadDeadline(time.Now())
				}
			}
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
	err = options.send(ctx, transport, files, zmodem.ProgressPrinter(options.Progress))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func handleZmodemDownload(ctx context.Context, host sshconfig.Host, remote io.Reader, writer *serializedRemoteWriter, options *ZmodemOptions, header []byte) error {
	dir := options.DownloadDir(host)
	if dir == "" {
		return errors.New("download directory is empty")
	}
	transport := &zmodemTransport{
		Reader: &prefixReader{prefix: header, reader: remote},
		writer: writer,
	}
	_, err := options.receive(ctx, transport, dir, options.MaxFileSize, options.Overwrite, zmodem.ProgressPrinter(options.Progress))
	if ctx.Err() != nil {
		return ctx.Err()
	}
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

func (r *prefixReader) SetReadDeadline(deadline time.Time) error {
	if setter, ok := r.reader.(readDeadlineSetter); ok {
		return setter.SetReadDeadline(deadline)
	}
	return nil
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

func (t *zmodemTransport) SetReadDeadline(deadline time.Time) error {
	if setter, ok := t.Reader.(readDeadlineSetter); ok {
		return setter.SetReadDeadline(deadline)
	}
	return nil
}

var zmodemCancel = []byte{0x18, 0x18, 0x18, 0x18, 0x18}
