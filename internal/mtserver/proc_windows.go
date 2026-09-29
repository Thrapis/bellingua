//go:build windows

package mtserver

import (
	"fmt"
	"log/slog"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// configureProcAttr is a no-op on Windows; see bindToParent and stopProcess.
func configureProcAttr(*exec.Cmd) {}

// bindToParent puts the started process in a job object that kills its whole
// tree when the job's last handle closes — i.e. when bellingua exits in any
// way, including a crash or Task Manager. The handle is deliberately kept
// open for the life of the process.
func bindToParent(cmd *exec.Cmd, log *slog.Logger) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		log.Warn("lingvanex: no job object; the server may outlive a crash", "err", err)
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		log.Warn("lingvanex: job object limits", "err", err)
		windows.CloseHandle(job)
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		log.Warn("lingvanex: open process for job object", "err", err)
		windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		log.Warn("lingvanex: assign to job object", "err", err)
		windows.CloseHandle(job)
	}
}

// stopProcess kills the process and all its children with taskkill /T: `py`
// spawns a separate python.exe that a plain Process.Kill would orphan.
func stopProcess(cmd *exec.Cmd, exited <-chan struct{}, timeout time.Duration, log *slog.Logger) {
	pid := cmd.Process.Pid
	kill := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(pid))
	if out, err := kill.CombinedOutput(); err != nil {
		log.Warn("lingvanex: taskkill failed, falling back to Kill", "pid", pid, "err", err, "output", string(out))
		_ = cmd.Process.Kill()
	}
	select {
	case <-exited:
	case <-time.After(timeout):
		log.Warn("lingvanex: process did not exit before timeout", "pid", pid)
	}
}
