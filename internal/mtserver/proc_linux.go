//go:build linux

package mtserver

import (
	"log/slog"
	"os/exec"
	"syscall"
)

// configureProcAttr gives the child its own process group (signalled as a
// whole on Stop) and has the kernel kill it if bellingua dies first.
func configureProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func bindToParent(*exec.Cmd, *slog.Logger) {}
