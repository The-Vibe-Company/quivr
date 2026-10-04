//go:build !unix

package devhost

import (
	"os"
	"os/exec"
)

func setProcessGroup(*exec.Cmd) {}

func terminate(p *os.Process) error { return p.Kill() }

func kill(p *os.Process) error { return p.Kill() }
