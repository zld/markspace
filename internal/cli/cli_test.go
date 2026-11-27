package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// run executes the command in-process and returns stdout, stderr and the exit
// code, exactly as the terminal would see them.
func run(args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	code := Main(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestDemoSuccessAcceptance(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	stdout, stderr, code := run("demo", "--work", work,
		"--target-rate", "48000", "--target-layout", "stereo", "--lufs", "-16")
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	checks := map[string]string{
		"five stages":      "stages completed : 5 / 5",
		"result ok":        "result          : OK",
		"manifest valid":   "manifest check  : valid",
		"target rate":      "48000 Hz",
		"sha256 line":      "manifest sha256 : ",
		"resume offset":    "resume offset    : ",
		"frames before":    "frames ",
		"manifest version": "assetpack/v2",
	}
	for name, want := range checks {
		if !strings.Contains(stdout, want) {
			t.Fatalf("%s: output missing %q\n%s", name, want, stdout)
		}
	}
	// Every resource must be reported at 48000 Hz stereo.
	if strings.Contains(stdout, "-> 44100 Hz") || strings.Contains(stdout, "-> 32000 Hz") {
		t.Fatalf("an output rate other than 48000 appeared:\n%s", stdout)
	}
	if got := regexp.MustCompile(`manifest sha256 : ([0-9a-f]{64})`).FindStringSubmatch(stdout); got == nil {
		t.Fatalf("no full sha256 in output:\n%s", stdout)
	}
}

func TestDemoRerunReusesAll(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	args := []string{"demo", "--work", work, "--target-rate", "48000", "--target-layout", "stereo", "--lufs", "-16"}
	first, _, code := run(args...)
	if code != 0 {
		t.Fatalf("first run exit %d\n%s", code, first)
	}
	second, _, code := run(args...)
	if code != 0 {
		t.Fatalf("second run exit %d\n%s", code, second)
	}
	firstDigest := digestOf(t, first)
	secondDigest := digestOf(t, second)
	if firstDigest != secondDigest {
		t.Fatalf("digest changed between identical runs: %s vs %s", firstDigest, secondDigest)
	}
	if !strings.Contains(second, "added           : 0") {
		t.Fatalf("rerun added resources:\n%s", second)
	}
	if !regexp.MustCompile(`reused\s+: 3`).MatchString(second) {
		t.Fatalf("rerun did not report 3 reused resources:\n%s", second)
	}
}

func TestDemoBrokenAcceptance(t *testing.T) {
	work := filepath.Join(t.TempDir(), "broken")
	args := []string{"demo", "--work", work, "--target-rate", "48000",
		"--target-layout", "stereo", "--lufs", "-16",
		"--inject", "truncated-ts,cc-jump,psi-update", "--fail-after", "manifest-stage"}
	stdout, stderr, code := run(args...)
	// The scripted demo ends with the healthy resumed release, so a zero
	// exit code means the final state verified.  The damaged act must still
	// have failed and been rolled back; that is asserted on the act output.
	act2 := strings.Index(stdout, "== act 2")
	act3idx := strings.Index(stdout, "== act 3")
	if act2 < 0 || act3idx < 0 {
		t.Fatalf("acts missing from output:\n%s\n%s", stdout, stderr)
	}
	damaged := stdout[act2:act3idx]
	if strings.Contains(damaged, "result          : OK") {
		t.Fatalf("damaged act reported OK:\n%s", damaged)
	}
	if !strings.Contains(damaged, "result          : FAILED") {
		t.Fatalf("damaged act did not report failure:\n%s", damaged)
	}
	for _, kind := range []string{"truncated-ts", "cc-jump", "psi-update"} {
		if !strings.Contains(stdout, kind) {
			t.Fatalf("fault class %q not reported\n%s\n%s", kind, stdout, stderr)
		}
	}
	if !strings.Contains(stdout, "rollback verified") {
		t.Fatalf("rollback not verified:\n%s", stdout)
	}
	if code != 0 {
		t.Fatalf("final resumed state did not verify: exit %d\n%s\n%s", code, stdout, stderr)
	}
	// Act 3 must reach committed and valid.
	act3 := stdout[act3idx:]
	if !strings.Contains(act3, "stages completed : 5 / 5") {
		t.Fatalf("resumed run did not complete five stages:\n%s", act3)
	}
	if !regexp.MustCompile(`commit\s+done`).MatchString(act3) {
		t.Fatalf("resumed run did not commit:\n%s", act3)
	}
	if !strings.Contains(act3, "manifest check  : valid") {
		t.Fatalf("resumed manifest not valid:\n%s", act3)
	}
	if !strings.Contains(act3, "result          : OK") {
		t.Fatalf("resumed run did not report OK:\n%s", act3)
	}
	// The manifest on disk is the healthy one and verifies via the verify
	// subcommand.
	vout, verr, vcode := run("verify", "--work", work)
	if vcode != 0 {
		t.Fatalf("verify exit %d: %s %s", vcode, vout, verr)
	}
	if !strings.Contains(vout, "manifest valid") {
		t.Fatalf("verify did not confirm validity: %s", vout)
	}
}

func TestDemoInvalidArguments(t *testing.T) {
	cases := [][]string{
		{"demo", "--target-rate", "48000"},
		{"demo", "--work", filepath.Join(t.TempDir(), "w"), "--target-layout", "quad"},
		{"demo", "--work", filepath.Join(t.TempDir(), "w"), "--lufs", "5"},
		{"demo", "--work", filepath.Join(t.TempDir(), "w"), "--target-rate", "999999"},
		{"demo", "--work", filepath.Join(t.TempDir(), "w"), "--inject", "nonsense"},
		{"demo", "--work", filepath.Join(t.TempDir(), "w"), "--fail-after", "nonsense"},
	}
	for _, args := range cases {
		_, _, code := run(args...)
		if code == 0 {
			t.Fatalf("invalid invocation accepted: %v", args)
		}
		if code != 2 && code != 1 {
			t.Fatalf("unexpected exit code %d for %v", code, args)
		}
	}
}

func TestVerifyOnMissingRelease(t *testing.T) {
	work := filepath.Join(t.TempDir(), "empty")
	if _, _, code := run("verify", "--work", work); code == 0 {
		t.Fatal("verify succeeded with no release")
	}
}

func TestDemoForceDoesNotDuplicate(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	base := []string{"demo", "--work", work, "--target-rate", "48000", "--target-layout", "stereo", "--lufs", "-16"}
	first, _, code := run(base...)
	if code != 0 {
		t.Fatalf("first exit %d", code)
	}
	forced, _, code := run(append(base, "--force")...)
	if code != 0 {
		t.Fatalf("forced exit %d\n%s", code, forced)
	}
	if !strings.Contains(forced, "added           : 0") {
		t.Fatalf("force appended resources:\n%s", forced)
	}
	if digestOf(t, forced) != digestOf(t, first) {
		t.Fatal("force changed the manifest digest")
	}
	// The artifact directory holds exactly one file per resource.
	entries, err := os.ReadDir(filepath.Join(work, "release", "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("artifact directory has %d entries, want 3", len(entries))
	}
}

func TestMonoAnd51Targets(t *testing.T) {
	for _, layout := range []string{"mono", "5.1"} {
		work := filepath.Join(t.TempDir(), layout)
		out, errOut, code := run("demo", "--work", work, "--target-rate", "44100",
			"--target-layout", layout, "--lufs", "-18")
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s\n%s", layout, code, out, errOut)
		}
		target := "44100 Hz " + layout
		if !strings.Contains(out, target) {
			t.Fatalf("%s: output does not show %q\n%s", layout, target, out)
		}
	}
}

func digestOf(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`manifest sha256 : ([0-9a-f]{64})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no manifest digest in output:\n%s", out)
	}
	return m[1]
}
