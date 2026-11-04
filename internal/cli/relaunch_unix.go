//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// relaunchProcess replaces the current process image. All old terminal input
// readers disappear atomically, which avoids a stale Bubble Tea read loop
// consuming the first key after an SSH session.
func relaunchProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(executable, os.Args, os.Environ())
}
