package adb

import (
	"os/exec"
	"syscall"
)

// hideWindow stops adb from flashing a console window when the desktop
// client runs without one.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
