//go:build windows

package cli

import (
	"os"
	"os/exec"
)

// relaunchProcess starts a fresh process and exits the current one. The parent
// must terminate because Windows cannot replace the current process image, and
// leaving it alive would retain the stale terminal input reader.
func relaunchProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
