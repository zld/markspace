package quality

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBitrateError(t *testing.T) {
	if got := BitrateError(105, 100); got != 5 {
		t.Fatalf("got %f", got)
	}
	if got := BitrateError(95, 100); got != -5 {
		t.Fatalf("got %f", got)
	}
	if got := BitrateError(100, 0); got != 0 {
		t.Fatalf("got %f", got)
	}
}

func TestAlignmentSamples(t *testing.T) {
	if got := AlignmentSamples(100, 99); got != 1 {
		t.Fatalf("got %d", got)
	}
	if got := AlignmentSamples(99, 100); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("hash = %s", got)
	}
	if _, err := HashFile(path + ".missing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateTolerances(t *testing.T) {
	ok := Stats{BitrateErrorPct: 5, ManifestAlignmentSample: 1}
	if err := ok.Validate(0, true); err != nil {
		t.Fatal(err)
	}
	highRate := Stats{BitrateErrorPct: 5.01}
	if err := highRate.Validate(0, true); err == nil {
		t.Fatal("expected bitrate failure")
	}
	misaligned := Stats{ManifestAlignmentSample: 2}
	if err := misaligned.Validate(0, false); err == nil {
		t.Fatal("expected alignment failure")
	}
}

func TestSummaryContainsContractFields(t *testing.T) {
	s := Stats{Packets: 1, OutputSHA256: "deadbeef", InputLUFS: -20, OutputLUFS: -23}
	out := s.Summary()
	for _, field := range []string{
		"packets=", "loss=", "duplicates=", "late_replays=", "bad_packets=",
		"input_bitrate_bps=", "target_bitrate_bps=", "bitrate_error_pct=",
		"input_lufs=", "output_lufs=", "peak_dbfs=", "clipped_samples=",
		"manifest_alignment_samples=", "output_sha256=",
	} {
		if !contains(out, field) {
			t.Fatalf("missing %q in:\n%s", field, out)
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
