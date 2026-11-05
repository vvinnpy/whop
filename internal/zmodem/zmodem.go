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

// ProgressPrinter returns a simple human-readable progress callback. It keeps
// one dynamic line so raw-mode terminals do not accumulate indentation and file
// listings do not grow by one line per block.
func ProgressPrinter(out io.Writer) ProgressFunc {
	var lastUpdate time.Time
	var lastLen int
	return func(progress Progress) {
		if progress.Done {
			line := fmt.Sprintf("%s: done (%d bytes)", progress.Name, progress.Transferred)
			if progress.Err != nil {
				line = fmt.Sprintf("%s: %v", progress.Name, progress.Err)
			}
			if lastLen > len(line) {
				line += strings.Repeat(" ", lastLen-len(line))
			}
			fmt.Fprintf(out, "\r%s\r\n", line)
			return
		}
		now := time.Now()
		if !lastUpdate.IsZero() && now.Sub(lastUpdate) < 100*time.Millisecond {
			return
		}
		lastUpdate = now
		line := fmt.Sprintf("%s: %d/%d bytes", progress.Name, progress.Transferred, progress.Total)
		lastLen = len(line)
		fmt.Fprintf(out, "\r%s", line)
	}
}

// DefaultTimeout is a sensible receive timeout for real transports.
const DefaultTimeout = 30 * time.Second
