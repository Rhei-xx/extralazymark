//go:build unix

package gitsync

import (
	"os/exec"
	"syscall"
)

// git starts helpers of its own, ssh above all. Stopping git alone can leave
// a helper running, so a git that must not outlive the program is started in
// its own process group, and the whole group is signalled together.

// ownGroup makes cmd start in a new process group.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interruptGroup asks cmd and everything it started to stop.
func interruptGroup(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

// killGroup ends whatever is left of cmd's process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
