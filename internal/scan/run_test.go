package scan

import (
	"path/filepath"
	"testing"
)

func TestRunDemoHealthyMetricsAndCorruptExit(t *testing.T) {
	out := t.TempDir()
	res, err := Run(out, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 2 {
		t.Fatalf("exit code = %d, want 2 (corrupt input recorded)", res.ExitCode)
	}
	if res.NormalTotal != 12 || res.NormalIndexed != 12 {
		t.Fatalf("normal samples = %d/%d, want 12/12", res.NormalIndexed, res.NormalTotal)
	}
	if !res.HashMatch || res.HashFailed != 0 {
		t.Fatalf("hash match = %v failed=%d, want all match", res.HashMatch, res.HashFailed)
	}
	if res.Summary.KeyframeHits != res.Summary.KeyframeWanted {
		t.Fatalf("keyframe hits %d/%d, want 100%%",
			res.Summary.KeyframeHits, res.Summary.KeyframeWanted)
	}
	if res.Summary.Misalignments != 0 {
		t.Fatalf("misalignments = %d, want 0 for aligned fixtures",
			res.Summary.Misalignments)
	}
	// Every corrupt MP4 produced a located offset; no Unknown (-1) shown.
	if len(res.ErrorOffsets) == 0 {
		t.Fatal("corrupt fixtures must report offsets")
	}
	for _, off := range res.ErrorOffsets {
		if off < 0 {
			t.Fatalf("unknown offset %d must be filtered from output", off)
		}
	}
}

func TestResumeReusesAndStaysIdentical(t *testing.T) {
	out := t.TempDir()
	first, err := Run(out, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(out, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Summary.ReusedChunks == 0 {
		t.Fatal("resume must reuse committed chunks")
	}
	if len(first.Summary.Hashes) != len(second.Summary.Hashes) {
		t.Fatal("resumed run changed the record count")
	}
	for i := range first.Summary.Hashes {
		if first.Summary.Hashes[i].Hash != second.Summary.Hashes[i].Hash {
			t.Fatalf("hash %d differs after resume", i)
		}
	}
	if first.ExitCode != second.ExitCode {
		t.Fatalf("exit codes differ across runs: %d vs %d",
			first.ExitCode, second.ExitCode)
	}
}

func TestVerifyMatchesWrittenIndex(t *testing.T) {
	out := t.TempDir()
	if _, err := Run(out, 2, false); err != nil {
		t.Fatal(err)
	}
	code, report, err := Verify(out)
	if err != nil || code != 0 {
		t.Fatalf("verify code=%d err=%v report=%s", code, err, report)
	}
}

func TestVerifyMissingIndexIsArgError(t *testing.T) {
	out := t.TempDir()
	code, _, err := Verify(filepath.Join(out, "never-run"))
	if err == nil || code != 1 {
		t.Fatalf("missing index must give exit 1, got code=%d err=%v", code, err)
	}
}

func TestPrintSummaryContainsAllMetrics(t *testing.T) {
	out := t.TempDir()
	res, err := Run(out, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	text := res.PrintSummary()
	for _, want := range []string{
		"round-trip parse rate",
		"keyframe index hits",
		"sha-256 matches",
		"misalignments",
		"error offsets",
		"checkpoint resumes",
	} {
		if !contains(text, want) {
			t.Fatalf("summary missing metric %q:\n%s", want, text)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
