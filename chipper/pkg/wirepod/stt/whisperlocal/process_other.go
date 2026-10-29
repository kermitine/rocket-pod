//go:build !linux && !windows

package whisperlocal

import "os/exec"

func configureProcess(command *exec.Cmd) {}
