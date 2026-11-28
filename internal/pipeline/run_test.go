package pipeline

import (
	"bytes"
	"strings"
	"testing"

	"mediacheck/internal/codec"
)

func testOptions(dir string) Options {
	return Options{
		Workdir:       dir,
		Codecs:        []codec.Kind{codec.H264, codec.H265, codec.AV1},
		CorruptOffset: 37,
	}
}

func TestRunSuccess(t *testing.T) {
	var out bytes.Buffer
	code := Run(testOptions(t.TempDir()), &out)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	s := out.String()
	for _, want := range []string{
		"container track=1 codec=h264", "container track=2 codec=h265", "container track=3 codec=av1",
		"tracks mappings=3", "segment codec=av1", "corrupt codec=h264 offset=37 code=E_BAD_HEADER",
		"loudness peak_dbfs=-6.02", "publish release=r2", "boundary sample=truncated",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRunDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	if Run(testOptions(t.TempDir()), &a) != 0 || Run(testOptions(t.TempDir()), &b) != 0 {
		t.Fatal("runs failed")
	}
	if a.String() != b.String() {
		t.Fatal("same inputs must produce identical output")
	}
}

func TestRunInterruptRecover(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.InterruptAfter = "migrate-1"
	var out bytes.Buffer
	if code := Run(opts, &out); code != 3 {
		t.Fatalf("interrupt exit %d want 3\n%s", code, out.String())
	}
	// recovery in a second run over the same workdir state is covered
	// by the CLI; here recover within one run:
	opts.Recover = true
	out.Reset()
	if code := Run(opts, &out); code != 0 {
		t.Fatalf("recover exit %d want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "recover active=r1 intact=true") {
		t.Fatalf("missing recovery report\n%s", out.String())
	}
	if !strings.Contains(out.String(), "active_versions=1") {
		t.Fatalf("missing single-active-version report\n%s", out.String())
	}
}

func TestRunBudgetExceeded(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.BudgetBytes = 10 // far below what the pipeline processes
	var out bytes.Buffer
	if code := Run(opts, &out); code != 3 {
		t.Fatalf("exit %d want 3\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "E_BUDGET_EXCEEDED") {
		t.Fatalf("missing budget error\n%s", out.String())
	}
	if strings.Contains(out.String(), "publish release") {
		t.Fatal("nothing may be published when the budget is exceeded")
	}
}
