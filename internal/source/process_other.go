//go:build !windows

package source

import "os/exec"

func hideProcess(cmd *exec.Cmd) {}
