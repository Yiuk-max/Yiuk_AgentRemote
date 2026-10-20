//go:build !windows

package agent

import (
	"os"
	"os/exec"
)

func stopProcess(cmd *exec.Cmd) error { return cmd.Process.Signal(os.Interrupt) }
