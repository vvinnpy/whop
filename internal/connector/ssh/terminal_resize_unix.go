//go:build !windows

package sshconn

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// terminalResizeEvents reports local terminal window changes. SIGWINCH is
// delivered to the process rather than to a particular file descriptor.
func terminalResizeEvents() (<-chan struct{}, func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		for {
			select {
			case <-signals:
				select {
				case events <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()

	stop := func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
	return events, stop
}
