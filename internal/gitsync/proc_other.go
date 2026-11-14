//go:build !unix

package gitsync

import "os/exec"

// Process groups are a Unix feature. Elsewhere only git itself is stopped.

func ownGroup(cmd *exec.Cmd) {}

func interruptGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}

func killGroup(cmd *exec.Cmd) {}
