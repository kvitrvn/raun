//go:build unix

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes the command lead its own process group so that
// cancellation also kills the processes it spawned.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
