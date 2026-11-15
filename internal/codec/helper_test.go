package codec

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

// buildToBin compiles cmd/stubencoder from the module root into bin.
// Tests call this instead of relying on any preinstalled binary.
func buildToBin(bin string) error {
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile: <root>/internal/codec/runner_helper_test.go -> module root
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/stubencoder")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &buildError{string(out)}
	}
	return nil
}

type buildError struct{ msg string }

func (e *buildError) Error() string { return "go build stubencoder: " + e.msg }
