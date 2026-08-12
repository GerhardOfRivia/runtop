//go:build windows

package main

import (
	"os"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) {}

func signalProcessTree(pid int, force bool) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if force {
		return process.Kill()
	}
	return process.Signal(os.Interrupt)
}

func processGroupID(pid int) (int, error) {
	return pid, nil
}
