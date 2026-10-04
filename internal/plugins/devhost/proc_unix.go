//go:build unix

package devhost

import (
	"os"
	"os/exec"
	"syscall"
)

// The plugin runs in its own process group so that stopping it also stops
// processes its run command spawned (for example a wrapper script).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminate(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGTERM) }

func kill(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGKILL) }
