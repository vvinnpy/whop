// Package zmodem implements ZMODEM file transfer over terminal byte streams.
package zmodem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/natefinch/atomic"
	zm "github.com/xx25/go-zmodem"
)

// Progress describes one transfer progress event.
type Progress struct {
	Name        string
	Transferred int64
	Total       int64
	Done        bool
	Err         error
}

// ProgressFunc receives transfer progress.
type ProgressFunc func(Progress)

// Send offers local files to a remote ZMODEM receiver (the remote runs rz).
func Send(ctx context.Context, transport io.ReadWriter, files []string, progress ProgressFunc) error {
	if len(files) == 0 {
		return errors.New("zmodem: no files to send")
	}
	handler, err := newSender(files, progress)
	if err != nil {
		return err
	}
	defer handler.Close()
	session := zm.NewSession(transport, handler, &zm.Config{
		Use32BitCRC:      true,
		MaxBlockSize:     8192,
		MaxRetries:       10,
		RecvTimeout:      DefaultTimeout,
		DataRecvTimeout:  2 * DefaultTimeout,
		DataStallTimeout: 2 * DefaultTimeout,
	})
	return session.Send(ctx)
}

// Receive receives files into destDir from a remote ZMODEM sender (the remote
// runs sz). Existing files are skipped unless overwrite is true.
func Receive(ctx context.Context, transport io.ReadWriter, destDir string, maxFileSize int64, overwrite bool, progress ProgressFunc) ([]string, error) {
	if destDir == "" {
		return nil, errors.New("zmodem: empty destination directory")
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return nil, fmt.Errorf("zmodem: create destination: %w", err)
	}
	handler := &receiver{
		destDir:     destDir,
		maxFileSize: maxFileSize,
		overwrite:   overwrite,
		progress:    progress,
	}
	defer handler.cleanup()
	session := zm.NewSession(transport, handler, &zm.Config{
		Use32BitCRC:      true,
		MaxBlockSize:     8192,
		MaxFileSize:      maxFileSize,
		MaxRetries:       10,
		RecvTimeout:      DefaultTimeout,
		DataRecvTimeout:  2 * DefaultTimeout,
		DataStallTimeout: 2 * DefaultTimeout,
	})
	err := session.Receive(ctx)
	return handler.completed, err
}

type sender struct {
	files    []*zm.FileOffer
	index    int
	progress ProgressFunc
}

func newSender(paths []string, progress ProgressFunc) (*sender, error) {
	handler := &sender{progress: progress}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("zmodem: open %s: %w", path, err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("zmodem: stat %s: %w", path, err)
		}
		if info.IsDir() {
			_ = file.Close()
			return nil, fmt.Errorf("zmodem: %s is a directory", path)
		}
		handler.files = append(handler.files, &zm.FileOffer{
			Name:    filepath.Base(path),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Mode:    uint32(info.Mode().Perm()),
			Reader:  file,
		})
	}
	return handler, nil
}

func (s *sender) Close() error {
	var first error
	for _, offer := range s.files {
		if file, ok := offer.Reader.(*os.File); ok {
			if err := file.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (s *sender) NextFile() *zm.FileOffer {
	if s.index >= len(s.files) {
		return nil
	}
	offer := s.files[s.index]
	s.index++
	return offer
}

func (s *sender) AcceptFile(zm.FileInfo) (io.WriteCloser, int64, error) {
	return nil, 0, zm.ErrSkip
}

func (s *sender) FileProgress(info zm.FileInfo, transferred int64) {
	if s.progress != nil {
		s.progress(Progress{Name: info.Name, Transferred: transferred, Total: info.Size})
	}
}

func (s *sender) FileCompleted(info zm.FileInfo, transferred int64, err error) {
	if s.progress != nil {
		s.progress(Progress{Name: info.Name, Transferred: transferred, Total: info.Size, Done: true, Err: err})
	}
}

type receiver struct {
	destDir     string
	maxFileSize int64
	overwrite   bool
	progress    ProgressFunc
	completed   []string
	pending     map[string]*pendingReceive
}

type pendingReceive struct {
	finalPath string
	tempPath  string
	file      *os.File
}

func (r *receiver) NextFile() *zm.FileOffer { return nil }

func (r *receiver) AcceptFile(info zm.FileInfo) (io.WriteCloser, int64, error) {
	if r.maxFileSize > 0 && info.Size > r.maxFileSize {
		return nil, 0, zm.ErrSkip
	}
	path, err := safeJoin(r.destDir, info.Name)
	if err != nil {
		return nil, 0, err
	}
	if !r.overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil, 0, zm.ErrSkip
		}
	}
	file, err := os.CreateTemp(r.destDir, ".whop-zmodem-*")
	if err != nil {
		return nil, 0, fmt.Errorf("zmodem: create temp file for %s: %w", path, err)
	}
	if r.pending == nil {
		r.pending = make(map[string]*pendingReceive)
	}
	r.pending[info.Name] = &pendingReceive{finalPath: path, tempPath: file.Name(), file: file}
	return file, 0, nil
}

func (r *receiver) FileProgress(info zm.FileInfo, transferred int64) {
	if r.progress != nil {
		r.progress(Progress{Name: info.Name, Transferred: transferred, Total: info.Size})
	}
}

func (r *receiver) FileCompleted(info zm.FileInfo, transferred int64, err error) {
	pending := r.pending[info.Name]
	if pending != nil {
		delete(r.pending, info.Name)
		_ = pending.file.Close()
		if err == nil {
			if !r.overwrite {
				if _, statErr := os.Stat(pending.finalPath); statErr == nil {
					err = zm.ErrSkip
				}
			}
		}
		if err == nil {
			if !info.ModTime.IsZero() {
				_ = os.Chtimes(pending.tempPath, info.ModTime, info.ModTime)
			}
			if info.Mode != 0 {
				_ = os.Chmod(pending.tempPath, os.FileMode(info.Mode).Perm())
			}
			if replaceErr := atomic.ReplaceFile(pending.tempPath, pending.finalPath); replaceErr != nil {
				err = fmt.Errorf("zmodem: finalize %s: %w", pending.finalPath, replaceErr)
			} else {
				r.completed = append(r.completed, pending.finalPath)
			}
		}
		if err != nil {
			_ = os.Remove(pending.tempPath)
		}
	}
	if r.progress != nil {
		r.progress(Progress{Name: info.Name, Transferred: transferred, Total: info.Size, Done: true, Err: err})
	}
}

func (r *receiver) cleanup() {
	for name, pending := range r.pending {
		_ = pending.file.Close()
		_ = os.Remove(pending.tempPath)
		delete(r.pending, name)
	}
}

func safeJoin(dir, name string) (string, error) {
	name = zm.SanitizeFilename(name)
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		return "", errors.New("zmodem: unsafe filename")
	}
	path := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("zmodem: unsafe path %q", name)
	}
	return path, nil
}

// WaitForHeader reports whether data contains a hexadecimal or binary ZMODEM
// header prefix.
func WaitForHeader(data []byte) bool {
	return bytes.Contains(data, []byte("**\x18B")) ||
		bytes.Contains(data, []byte("*\x18A")) ||
		bytes.Contains(data, []byte("*\x18C")) ||
		bytes.Contains(data, []byte("*\x18D"))
}

// ProgressPrinter returns a simple human-readable progress callback. Terminal
// writers get one dynamic line; redirected output gets only final status lines.
func ProgressPrinter(out io.Writer) ProgressFunc {
	return progressPrinter(out, isTerminalWriter(out))
}

func progressPrinter(out io.Writer, interactive bool) ProgressFunc {
	var lastUpdate time.Time
	var lastWidth int
	return func(progress Progress) {
		name := sanitizeProgressText(progress.Name)
		if name == "" {
			name = "file"
		}
		if progress.Done {
			line := sanitizeProgressText(fmt.Sprintf("%s: done (%d bytes)", name, progress.Transferred))
			if progress.Err != nil {
				line = sanitizeProgressText(fmt.Sprintf("%s: %v", name, progress.Err))
			}
			if interactive {
				width := ansi.StringWidth(line)
				if lastWidth > width {
					line += strings.Repeat(" ", lastWidth-width)
				}
				fmt.Fprintf(out, "\r%s\r\n", line)
			} else {
				fmt.Fprintln(out, line)
			}
			return
		}
		if !interactive {
			return
		}
		now := time.Now()
		if !lastUpdate.IsZero() && now.Sub(lastUpdate) < 100*time.Millisecond {
			return
		}
		lastUpdate = now
		line := sanitizeProgressText(fmt.Sprintf("%s: %d/%d bytes", name, progress.Transferred, progress.Total))
		lastWidth = ansi.StringWidth(line)
		fmt.Fprintf(out, "\r%s", line)
	}
}

func sanitizeProgressText(text string) string {
	var out strings.Builder
	lastSpace := false
	for _, r := range text {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if out.Len() > 0 && !lastSpace {
				out.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		out.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(out.String())
}

func isTerminalWriter(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// DefaultTimeout is a sensible receive timeout for real transports.
const DefaultTimeout = 30 * time.Second
