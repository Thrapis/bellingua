//go:build !windows && !linux

package mtserver

import (
	"log/slog"
	"os/exec"
	"syscall"
)

// configureProcAttr puts the child in its own process group so the whole
// group can be signalled at once on Stop.
func configureProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func bindToParent(*exec.Cmd, *slog.Logger) {}
