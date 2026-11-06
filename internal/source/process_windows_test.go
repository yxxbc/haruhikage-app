package source

import (
	"os/exec"
	"testing"
)

func TestWorkerWindowsLaunchIsHidden(t *testing.T) {
	// Source-host receives both Windows hiding flags before process creation.
	cmd := exec.Command("source-host.exe")
	hideProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("worker can create a visible console")
	}
}
