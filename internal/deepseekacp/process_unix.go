//go:build !windows

package deepseekacp

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func ownProcess(ctx context.Context, cmd *exec.Cmd) (func() error, func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// DSH owns detached shell ranges as well as children in this group. Give
	// its signal handler time to dispose those ranges before forced cleanup.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return func() error { return ctx.Err() }, func() { _ = kill() }, nil
}
