package sshconn

import (
	"io"
	"os"
	"sync"
	"time"
)

const remoteStreamBufferLimit = 256 * 1024

// remoteStream owns the blocking SSH reads in a background pump.  The ZMODEM
// library needs SetReadDeadline support, which the SSH session pipes do not
// provide; buffering the stream lets a deadline interrupt only the current
// consumer without losing bytes for the shell that resumes afterwards.
type remoteStream struct {
	src     io.Reader
	writer  io.Writer
	closeFn func() error

	mu       sync.Mutex
	buf      []byte
	err      error
	deadline time.Time
	wake     chan struct{}
	space    chan struct{}
	done     chan struct{}

	closeOnce sync.Once
}

func newRemoteStream(src io.Reader, writer io.Writer, closeFn func() error) *remoteStream {
	s := &remoteStream{
		src:     src,
		writer:  writer,
		closeFn: closeFn,
		wake:    make(chan struct{}, 1),
		space:   make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go s.pump()
	return s
}

func (s *remoteStream) pump() {
	buffer := make([]byte, 32*1024)
	for {
		n, err := s.src.Read(buffer)
		if n > 0 {
			s.mu.Lock()
			s.buf = append(s.buf, buffer[:n]...)
			s.mu.Unlock()
			notify(s.wake)
		}
		if err != nil {
			s.mu.Lock()
			if s.err == nil {
				s.err = err
			}
			s.mu.Unlock()
			notify(s.wake)
			return
		}
		for {
			s.mu.Lock()
			size := len(s.buf)
			s.mu.Unlock()
			if size <= remoteStreamBufferLimit {
				break
			}
			select {
			case <-s.space:
			case <-s.done:
				return
			}
		}
	}
}

func (s *remoteStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			s.mu.Unlock()
			notify(s.space)
			return n, nil
		}
		if s.err != nil {
			err := s.err
			s.mu.Unlock()
			return 0, err
		}
		deadline := s.deadline
		s.mu.Unlock()

		if !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			timer := time.NewTimer(wait)
			select {
			case <-s.wake:
				timer.Stop()
			case <-timer.C:
				return 0, os.ErrDeadlineExceeded
			case <-s.done:
				timer.Stop()
				return 0, io.ErrClosedPipe
			}
			continue
		}

		select {
		case <-s.wake:
		case <-s.done:
			return 0, io.ErrClosedPipe
		}
	}
}

func (s *remoteStream) Write(p []byte) (int, error) {
	return s.writer.Write(p)
}

func (s *remoteStream) SetReadDeadline(deadline time.Time) error {
	s.mu.Lock()
	s.deadline = deadline
	s.mu.Unlock()
	notify(s.wake)
	return nil
}

func (s *remoteStream) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		notify(s.wake)
		if closer, ok := s.src.(io.Closer); ok {
			closeErr = closer.Close()
		}
		if s.closeFn != nil {
			if err := s.closeFn(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
