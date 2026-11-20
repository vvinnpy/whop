//go:build windows

package sshconn

import (
	"sync"
	"time"
)

const terminalResizePollInterval = 250 * time.Millisecond

// terminalResizeEvents polls on Windows because console resize notifications
// are not exposed through os/signal. watchTerminalSize compares dimensions, so
// polling only sends a window-change request when the size actually differs.
func terminalResizeEvents() (<-chan struct{}, func()) {
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		ticker := time.NewTicker(terminalResizePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
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
			close(done)
		})
	}
	return events, stop
}
