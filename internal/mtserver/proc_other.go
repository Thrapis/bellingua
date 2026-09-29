//go:build !windows

package mtserver

import (
	"log/slog"
	"os/exec"
	"syscall"
	"time"
)

// stopProcess sends SIGTERM to the group, waits up to timeout, then SIGKILLs.
func stopProcess(cmd *exec.Cmd, exited <-chan struct{}, timeout time.Duration, log *slog.Logger) {
	pid := cmd.Process.Pid
	signal := func(sig syscall.Signal) {
		if err := syscall.Kill(-pid, sig); err != nil {
			_ = cmd.Process.Signal(sig)
		}
	}
	signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(timeout):
		log.Warn("lingvanex: SIGTERM timed out, sending SIGKILL", "pid", pid)
		signal(syscall.SIGKILL)
		<-exited
	}
}
