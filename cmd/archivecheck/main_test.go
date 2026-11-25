package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"archivecheck/internal/fixture"
)

func TestRunExitCodeContract(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("no args code=%d want2", code)
	}

	out.Reset()
	errOut.Reset()
	code := run([]string{"-input", "manifest.json", "-bitrates", "nonsense"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("bad bitrates code=%d want2 stderr=%s", code, errOut.String())
	}

	dir := t.TempDir()
	out.Reset()
	errOut.Reset()
	code = run([]string{"-generate", filepath.Join(dir, "fixtures")}, &out, &errOut)
	if code != 0 {
		t.Fatalf("generate code=%d want0 stderr=%s", code, errOut.String())
	}
}

func TestVerifyGeneratedManifestThroughCLI(t *testing.T) {
	root := t.TempDir()
	fx, err := fixture.Generate(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "out")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"-input", fx.ValidInput, "-out", outDir, "-bitrates", "400000,900000",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("process code=%d logs=%s err=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := run([]string{"-verify", filepath.Join(outDir, "manifest.json")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}
