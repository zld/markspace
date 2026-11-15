package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// requiredPlaceholders must all appear in an encoder command template so the
// runner can pass discrete, non-shell-interpreted arguments.
var requiredPlaceholders = []string{"{in}", "{out}", "{kbps}", "{duration}"}

// stubTemplate is the argument shape used for the bundled encoder double.
const stubTemplate = "--in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}"

// ResolveEncoder guarantees an explicit encoder template. When the user
// supplies --encoder it is validated; otherwise the demo builds the bundled
// stubencoder into the workspace and references it by absolute path. Either
// way the resolved command is explicit, local and shell-free.
func (o *Options) ResolveEncoder(stderr ioWriter) error {
	if o.Encoder != "" {
		return validateTemplate(o.Encoder)
	}
	bin, err := buildBundledEncoder(o.Workspace)
	if err != nil {
		return err
	}
	o.EncoderBin = bin
	o.Encoder = bin + " " + stubTemplate
	fmt.Fprintf(stderr,
		"transcode: no --encoder given; using bundled synthetic test double: %s\n", bin)
	fmt.Fprintln(stderr,
		"transcode: the double quantizes sample frames; it is not a real codec and its PSNR is synthetic.")
	return nil
}

func validateTemplate(tmpl string) error {
	fields := strings.Fields(tmpl)
	if len(fields) == 0 {
		return fmt.Errorf("%w: empty encoder template", errInvalid)
	}
	for _, ph := range requiredPlaceholders {
		if !contains(fields, ph) {
			return fmt.Errorf("%w: encoder template must include %s", errInvalid, ph)
		}
	}
	return nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// buildBundledEncoder compiles cmd/stubencoder into the workspace bin
// directory. The output path is deterministic, so a repeat run reuses it.
func buildBundledEncoder(workspace string) (string, error) {
	binDir := filepath.Join(workspace, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	bin := filepath.Join(binDir, "stubencoder")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if _, err := os.Stat(bin); err == nil {
		return bin, nil // already built in this workspace
	}
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/stubencoder")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build bundled encoder: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return bin, nil
}

// moduleRoot locates the repository root relative to this source file, so
// the auto-build works no matter the process's current working directory.
func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot resolve module root")
	}
	// file: <root>/internal/cli/encoder.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file))), nil
}
