package main

import (
	"fmt"
	"os/exec"
)

// resolveBinary locates a required external tool on PATH. All invocations use
// an absolute argv later; input bytes are never passed through a shell.
func resolveBinary(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH; install it first", name)
	}
	return p, nil
}
