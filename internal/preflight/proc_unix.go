//go:build unix

package preflight

import (
	"os/exec"
	"syscall"
)

// isolate gives the command its own process group, so a deadline or
// cancellation also stops anything it spawned (a benchmark's workers, say)
// rather than leaving it running beside the application.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
