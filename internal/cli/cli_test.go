package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = Main(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

var (
	reInt    = regexp.MustCompile(`(?m)^representations=(\d+)$`)
	reSegs   = regexp.MustCompile(`(?m)^segments=(\d+)$`)
	reDrift  = regexp.MustCompile(`(?m)^max_drift_ms=(\d+)$`)
	reFaults = regexp.MustCompile(`(?m)^faults=(\d+)$`)
	reOffErr = regexp.MustCompile(`(?m)^offset_error_bytes=(\d+)$`)
	reResume = regexp.MustCompile(`(?m)^resume_ms=(\d+)$`)
	reSHA    = regexp.MustCompile(`(?m)^manifest_sha256=([0-9a-f]{64})$`)
)

func TestDemoAndVerify(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	code, stdout, stderr := runCLI(t, "demo", "--work", work, "--faults", "all")
	if code != ExitOK {
		t.Fatalf("demo exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	checks := []struct {
		name string
		re   *regexp.Regexp
		want string
	}{
		{"representations", reInt, "3"},
		{"segments", reSegs, "12"},
		{"faults", reFaults, "5"},
		{"offset_error_bytes", reOffErr, "0"},
	}
	for _, c := range checks {
		m := c.re.FindStringSubmatch(stdout)
		if m == nil {
			t.Fatalf("output lacks %s\n%s", c.name, stdout)
		}
		if m[1] != c.want {
			t.Fatalf("%s = %s, want %s", c.name, m[1], c.want)
		}
	}
	if m := reDrift.FindStringSubmatch(stdout); m == nil || m[1] != "0" {
		t.Fatalf("max_drift_ms missing or above tolerance:\n%s", stdout)
	}
	if m := reResume.FindStringSubmatch(stdout); m == nil || len(m[1]) == 0 {
		t.Fatalf("resume_ms missing:\n%s", stdout)
	}
	m := reSHA.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("manifest_sha256 missing\n%s", stdout)
	}

	manifestPath := filepath.Join(work, "manifest.json")
	code, verifyOut, verifyErr := runCLI(t, "verify", "--work", work, "--manifest", manifestPath)
	if code != ExitOK {
		t.Fatalf("verify exit = %d\nstdout:\n%s\nstderr:\n%s", code, verifyOut, verifyErr)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if !strings.Contains(verifyOut, "manifest_sha256="+hex.EncodeToString(sum[:])) {
		t.Fatalf("verify did not recompute the checksum:\n%s", verifyOut)
	}
	if !strings.Contains(verifyOut, "verified=true") {
		t.Fatalf("verify did not confirm:\n%s", verifyOut)
	}
}

func TestDemoIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	var shas []string
	for i := 0; i < 2; i++ {
		work := filepath.Join(dir, "run")
		os.RemoveAll(work)
		code, stdout, stderr := runCLI(t, "demo", "--work", work, "--faults", "none")
		if code != ExitOK {
			t.Fatalf("demo exit = %d: %s", code, stderr)
		}
		shas = append(shas, reSHA.FindStringSubmatch(stdout)[1])
	}
	if shas[0] != shas[1] {
		t.Fatalf("manifest checksum changed across runs: %s vs %s", shas[0], shas[1])
	}
}

func TestVerifyRejectsTamperedManifest(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	if code, _, stderr := runCLI(t, "demo", "--work", work, "--faults", "none"); code != ExitOK {
		t.Fatalf("demo failed: %s", stderr)
	}
	manifestPath := filepath.Join(work, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["schema"] = "bogus/v0"
	tampered, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(manifestPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runCLI(t, "verify", "--work", work, "--manifest", manifestPath)
	if code != ExitManifestBad {
		t.Fatalf("verify exit = %d, want %d", code, ExitManifestBad)
	}
}

func TestVerifyRejectsChangedInput(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	if code, _, stderr := runCLI(t, "demo", "--work", work, "--faults", "none"); code != ExitOK {
		t.Fatalf("demo failed: %s", stderr)
	}
	mpd := filepath.Join(work, "inputs", "manifest.mpd")
	data, err := os.ReadFile(mpd)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\n<!-- edited after delivery -->\n")...)
	if err := os.WriteFile(mpd, data, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runCLI(t, "verify", "--work", work, "--manifest", filepath.Join(work, "manifest.json"))
	if code != ExitManifestBad {
		t.Fatalf("verify exit = %d, want %d", code, ExitManifestBad)
	}
}

func TestDemoNoFaults(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	code, stdout, stderr := runCLI(t, "demo", "--work", work, "--faults", "none")
	if code != ExitOK {
		t.Fatalf("demo exit = %d: %s", code, stderr)
	}
	if reFaults.MatchString(stdout) {
		t.Fatalf("faults printed without --faults all:\n%s", stdout)
	}
}

func TestVerifyRejectsPathEscape(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	code, _, _ := runCLI(t, "verify", "--work", work, "--manifest", "/etc/passwd")
	if code != ExitInputError {
		t.Fatalf("verify exit = %d, want %d", code, ExitInputError)
	}
}

func TestUsageErrors(t *testing.T) {
	if code, _, _ := runCLI(t); code != ExitInputError {
		t.Fatalf("no args exit = %d", code)
	}
	if code, _, _ := runCLI(t, "demo"); code != ExitInputError {
		t.Fatalf("demo without --work exit = %d", code)
	}
	if code, _, _ := runCLI(t, "demo", "--work", t.TempDir(), "--faults", "bogus"); code != ExitInputError {
		t.Fatalf("bad faults exit = %d", code)
	}
	if code, _, _ := runCLI(t, "verify", "--work", t.TempDir()); code != ExitInputError {
		t.Fatalf("verify without --manifest exit = %d", code)
	}
}
