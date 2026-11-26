package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"rtpregress/internal/fixture"
	"rtpregress/internal/ts"
)

func ffmpegPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	return p
}

// writeTrace builds one fixture case into a temp file and returns its path.
func writeTrace(t *testing.T, name string) string {
	t.Helper()
	raw, err := fixture.BuildTrace(context.Background(), ffmpegPath(t),
		fixture.Cases()[name])
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name+".rtp")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEndToEndCleanMeetsQualityThresholds(t *testing.T) {
	in := writeTrace(t, fixture.CaseClean)
	out := filepath.Join(t.TempDir(), "out")
	res, err := Run(context.Background(), Options{
		InPath: in, OutDir: out, Rate: 48000, Channels: 1,
		Bitrate: 96000, Workers: 4, Policy: ts.PolicyIsolate,
	})
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if res.ExitCode != ExitOK {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "output.m4s")); err != nil {
		t.Fatalf("fmp4 missing: %v", err)
	}
	m := res.Metrics
	if absF(m.BitrateDeltaPct) > 5.0 {
		t.Errorf("bitrate delta %.2f%% exceeds 5%%", m.BitrateDeltaPct)
	}
	if m.SNRDB < 15.0 {
		t.Errorf("SNR %.2f < 15 dB", m.SNRDB)
	}
	if absF(m.LUFS-(-16.0)) > 1.0 {
		t.Errorf("LUFS %.2f off target by >1 LU", m.LUFS)
	}
	if m.TruePeakDBTP > -1.0 {
		t.Errorf("true peak %.2f > -1 dBTP", m.TruePeakDBTP)
	}
	if m.ManifestMaxErrorMS > 20.0 {
		t.Errorf("manifest max error %.2f ms > 20", m.ManifestMaxErrorMS)
	}
}

func TestStrictPolicyReturnsExitTwo(t *testing.T) {
	in := writeTrace(t, fixture.CaseDamaged)
	out := filepath.Join(t.TempDir(), "strict")
	res, err := Run(context.Background(), Options{
		InPath: in, OutDir: out, Rate: 48000, Channels: 1,
		Bitrate: 96000, Workers: 2, Policy: ts.PolicyStrict,
	})
	if err == nil {
		t.Fatal("strict on damaged input must error")
	}
	if res == nil || res.ExitCode != ExitStrictInput {
		t.Fatalf("want exit %d, got res=%v err=%v", ExitStrictInput, res, err)
	}
}

func TestIsolateOnDamagedStillOutputs(t *testing.T) {
	in := writeTrace(t, fixture.CaseDamaged)
	out := filepath.Join(t.TempDir(), "iso")
	res, err := Run(context.Background(), Options{
		InPath: in, OutDir: out, Rate: 48000, Channels: 1,
		Bitrate: 96000, Workers: 2, Policy: ts.PolicyIsolate,
	})
	if err != nil {
		t.Fatalf("isolate must complete: %v", err)
	}
	if res.Stats.Gaps == 0 {
		t.Fatal("damaged trace must report gaps")
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatalf("manifest must still be written: %v", err)
	}
}

func TestCancelThenResumeProducesSameHash(t *testing.T) {
	in := writeTrace(t, fixture.CaseCancel)
	out := filepath.Join(t.TempDir(), "resume")
	// first run: cancel after 2 newly completed segments
	canceled, err := Run(context.Background(), Options{
		InPath: in, OutDir: out, Rate: 48000, Channels: 1,
		Bitrate: 96000, Workers: 2, Policy: ts.PolicyIsolate, CancelAfter: 2,
	})
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if canceled.ExitCode != ExitCanceled || !canceled.Canceled {
		t.Fatalf("want canceled exit %d, got %+v", ExitCanceled, canceled)
	}
	if _, err := os.Stat(CheckpointPath(out)); err != nil {
		t.Fatalf("checkpoint must be saved: %v", err)
	}
	partialHash := canceled.Checkpoint.RunHash

	// resume with a single worker to prove worker-count independence
	resumed, err := Run(context.Background(), Options{
		InPath: in, OutDir: out, Rate: 48000, Channels: 1,
		Bitrate: 96000, Workers: 1, Policy: ts.PolicyIsolate, Resume: true,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.ExitCode != ExitOK {
		t.Fatalf("resume exit = %d", resumed.ExitCode)
	}

	// a fresh full run with 4 workers must reach the same hash
	freshOut := filepath.Join(t.TempDir(), "fresh")
	fresh, err := Run(context.Background(), Options{
		InPath: in, OutDir: freshOut, Rate: 48000, Channels: 1,
		Bitrate: 96000, Workers: 4, Policy: ts.PolicyIsolate,
	})
	if err != nil {
		t.Fatalf("fresh run: %v", err)
	}
	if resumed.Checkpoint.RunHash != fresh.Checkpoint.RunHash {
		t.Fatalf("resumed hash %s != fresh hash %s",
			resumed.Checkpoint.RunHash, fresh.Checkpoint.RunHash)
	}
	if partialHash == resumed.Checkpoint.RunHash {
		t.Fatal("partial hash must differ from the completed run")
	}
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
