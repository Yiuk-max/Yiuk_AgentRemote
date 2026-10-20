//go:build windows

package agent

import "os/exec"

func stopProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }
