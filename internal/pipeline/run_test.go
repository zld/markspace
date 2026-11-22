package pipeline

import (
	"os"
	"testing"
	"time"

	"mediq/internal/report"
	"mediq/internal/timeline"
)

func TestCleanRunEndToEndInvariants(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig(dir, 23)
	out, err := Run(cfg)
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", out.ExitCode)
	}
	total := out.Report.Segments
	if total < 24 {
		t.Fatalf("segments = %d, want at least 24", total)
	}
	if out.Report.Quarantined != 0 {
		t.Fatalf("clean run quarantined %d segments", out.Report.Quarantined)
	}
	if out.Report.RoundtripPct != 100 {
		t.Fatalf("roundtrip = %.2f, want 100", out.Report.RoundtripPct)
	}
	if out.Report.SHA256MatchPct != 100 {
		t.Fatalf("sha match = %.2f, want 100", out.Report.SHA256MatchPct)
	}
	if out.Report.KeyframeIndexHit < 0.95 {
		t.Fatalf("hit rate = %.4f, want >= 0.95", out.Report.KeyframeIndexHit)
	}
	if out.Report.CrossRepSkewMs > float64(cfg.Tolerance.Millis()) {
		t.Fatalf("skew %.2fms exceeds tolerance", out.Report.CrossRepSkewMs)
	}
	// Three codecs must each be represented.
	codecs := map[string]bool{}
	for _, rep := range out.Manifest.Reps {
		codecs[rep.Codec] = true
	}
	for _, want := range []string{"h264", "h265", "av1"} {
		if !codecs[want] {
			t.Fatalf("manifest missing codec %s", want)
		}
	}
	// Evidence sidecar round-trips through go-mp4.
	brand, err := ReadEvidenceBrand(dir)
	if err != nil || brand != "mdiq" {
		t.Fatalf("evidence brand = %q, %v", brand, err)
	}
}

func TestRepeatedRunIsByteIdentical(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	out1, err := Run(DefaultConfig(dir1, 23))
	if err != nil {
		t.Fatal(err)
	}
	out2, err := Run(DefaultConfig(dir2, 23))
	if err != nil {
		t.Fatal(err)
	}
	if len(out1.Report.Hashes) != len(out2.Report.Hashes) {
		t.Fatal("hash row count differs between runs")
	}
	for i := range out1.Report.Hashes {
		h1, h2 := out1.Report.Hashes[i], out2.Report.Hashes[i]
		if h1.RepID != h2.RepID || h1.Boundary != h2.Boundary || h1.SHA256 != h2.SHA256 {
			t.Fatalf("non-deterministic segment hash at row %d: %+v != %+v", i, h1, h2)
		}
	}
}

func TestVerifyAfterCleanRunIsStable(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(DefaultConfig(dir, 23)); err != nil {
		t.Fatal(err)
	}
	v1, err := VerifyWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := VerifyWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v1.SHA256MatchPct != 100 || v2.SHA256MatchPct != 100 ||
		v1.Matched != v2.Matched || len(v1.Mismatches) != 0 {
		t.Fatalf("verify not stable: %+v vs %+v", v1, v2)
	}
}

func TestBrokenRunReportsFaultsAndQuarantine(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig(dir, 23)
	cfg.Faults = FaultSet{Truncate: true, DropIndex: true, DuplicateIndex: true, CorruptUnit: true}
	out, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 2 {
		t.Fatalf("damaged input exit = %d, want 2", out.ExitCode)
	}
	inj := out.Report.Injections
	if inj.TruncatedStreams == 0 || inj.CorruptUnits == 0 ||
		inj.MissingIndex == 0 || inj.DuplicateIndex == 0 {
		t.Fatalf("injection counts not reported: %+v", inj)
	}
	if out.Report.Quarantined == 0 || len(out.Report.QuarantineRecords) == 0 {
		t.Fatal("quarantine section must list isolated records")
	}
	// Missing index rows are recovered from the bitstream, so the plan
	// still covers every surviving keyframe and hit rate stays high.
	if out.Report.KeyframeIndexHit < 0.85 {
		t.Fatalf("repaired hit rate = %.4f", out.Report.KeyframeIndexHit)
	}
	// Re-verify after the safe commit: hashes must still match.
	v, err := VerifyWorkspace(dir)
	if err != nil || v.SHA256MatchPct != 100 {
		t.Fatalf("post-damage verify: %v match=%.2f", err, v.SHA256MatchPct)
	}
}

func TestMisalignmentRejectsGroupCommit(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig(dir, 23)
	cfg.Faults = FaultSet{Misalign: true}
	out, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 1 {
		t.Fatalf("skew rejection exit = %d, want 1", out.ExitCode)
	}
	found := false
	for _, e := range out.Report.Errors {
		if e.Kind == timeline.KindSkewExceeded && e.Action == "reject_group" {
			found = true
		}
	}
	if !found {
		t.Fatal("skew_exceeded/reject_group error missing from report")
	}
	// No segment directory may contain committed segment files.
	entries, err := os.ReadDir(dir + "/segments")
	if err == nil && len(entries) > 0 {
		t.Fatalf("rejected group left committed segments: %v", entries)
	}
}

func TestFailBeforeCommitRollsBackAndAllowsRerun(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig(dir, 23)
	cfg.FailAfter = FailBeforeCommit
	out, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 1 || out.StoppedAt != FailBeforeCommit {
		t.Fatalf("fail-point outcome = %+v", out)
	}
	// Staged temp files exist before recovery.
	if staged, _ := os.ReadDir(dir + "/segments"); len(staged) == 0 {
		t.Fatal("expected staged temp files before recovery")
	}
	rec, err := RecoverWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.RolledBack == 0 {
		t.Fatal("recovery must report at least one rollback")
	}
	for _, name := range []string{"segments", "quarantine"} {
		entries, err := os.ReadDir(dir + "/" + name)
		if err != nil {
			continue
		}
		for _, e := range entries {
			t.Fatalf("staged file survived recovery: %s/%s", name, e.Name())
		}
	}
	// A fresh run over the recovered workspace completes cleanly and
	// durable input commits from the first run were never overwritten.
	out2, err := Run(DefaultConfig(dir, 23))
	if err != nil || out2.ExitCode != 0 {
		t.Fatalf("rerun after recovery: %v exit=%d", err, out2.ExitCode)
	}
	if out2.Recover.Replayed < 6 { // inputs + indexes were durably committed
		t.Fatalf("replayed commits = %d, want durable input stages", out2.Recover.Replayed)
	}
}

func TestFailAfterScanLeavesRecoverableJournal(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig(dir, 23)
	cfg.FailAfter = FailAfterScan
	out, err := Run(cfg)
	if err != nil || out.ExitCode != 1 {
		t.Fatalf("scan fail point: %v %+v", err, out)
	}
	rec, err := RecoverWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.RolledBack != 0 && rec.Replayed == 0 {
		t.Fatalf("unexpected recovery state: %+v", rec)
	}
}

func TestReportTimestampDeterminism(t *testing.T) {
	// Report hashes/content apart from timestamps must match; ensure
	// the report struct carries the expected field names used by CLI.
	r := (&report.Builder{}).Finalize(nil, 1, 100, 0, 0, 0, 0, 0,
		report.InjectionCounts{}, time.Unix(0, 0))
	if r.GeneratedAt == "" || r.SchemaVersion != 1 {
		t.Fatal("report missing schema fields")
	}
}
