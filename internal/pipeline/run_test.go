package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/mp4"
)

func readFile(t *testing.T, p string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(p)
}

// buildInput constructs a small fragment group: two fragments where the
// second repeats the first's audio payload (duplicate content) and includes a
// run of zero-amplitude PCM (silence).
func buildInput(t *testing.T) []InputFile {
	t.Helper()
	// 16-bit LE PCM bytes. Loud sample 0x4000, silent zeros.
	loud := make([]byte, 8)
	silent := make([]byte, 16)
	for i := 0; i < 4; i++ {
		loud[i*2+1] = 0x40 // 0x4000 big-ish amplitude, LE high byte
	}
	// Fragment 1: one loud sample (8 bytes). Fragment 2 reuses the same loud
	// bytes (duplicate) plus a silent sample (16 bytes).
	f1 := mp4.BuildTestFragment(1, 0, len(loud))
	copy(f1[len(f1)-len(loud):], loud)

	f2Loud := mp4.BuildTestFragment(2, 1000, len(loud))
	copy(f2Loud[len(f2Loud)-len(loud):], loud)
	// Append a second moof/mdat fragment carrying silent PCM.
	f2Silent := mp4.BuildTestFragment(3, 2000, len(silent))
	copy(f2Silent[len(f2Silent)-len(silent):], silent)

	data := append(append(append([]byte{}, f1...), f2Loud...), f2Silent...)
	return []InputFile{{Name: "group.m4s", Data: data}}
}

func testCfg(workDir string) Config {
	return Config{
		WorkDir:          workDir,
		Timescales:       map[uint32]uint32{1: 48000},
		TargetSegmentUS:  20_000,
		DriftPPMMax:      1000,
		SilenceThreshold: 0.01,
		SilenceMinUS:     1,
		Limits:           mp4.Limits{},
	}
}

func TestRunEndToEndDeterministic(t *testing.T) {
	inputs := buildInput(t)

	res1, err := Run(inputs, testCfg(t.TempDir()))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	res2, err := Run(inputs, testCfg(t.TempDir()))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res1.ManifestSHA256 == "" || res1.ManifestSHA256 != res2.ManifestSHA256 {
		t.Fatalf("manifest checksum must be deterministic: %q vs %q",
			res1.ManifestSHA256, res2.ManifestSHA256)
	}
	if res1.OutputSHA256 != res1.ManifestSHA256 {
		t.Fatal("output and manifest checksums must agree")
	}
}

func TestRunProducesSegmentsSilenceAndDuplicate(t *testing.T) {
	inputs := buildInput(t)
	res, err := Run(inputs, testCfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) < 2 {
		t.Fatalf("want at least 2 segments, got %d", len(res.Segments))
	}
	if len(res.SilenceIntervals) < 1 {
		t.Fatalf("want at least 1 silence interval, got %d", len(res.SilenceIntervals))
	}
	// Two fragments carry identical loud PCM payloads -> one fingerprint.
	if len(res.Duplicates) == 0 {
		t.Fatal("expected at least one content fingerprint")
	}
	// The loud payload appears twice; silence adds distinct content, so the
	// loud fingerprint must collapse to a single entry.
	hashCount := map[string]int{}
	for _, fp := range res.Duplicates {
		hashCount[fp.Hash]++
	}
	for h, n := range hashCount {
		if n > 1 {
			t.Fatalf("fingerprint %s duplicated in output: %d", h, n)
		}
	}
}

func TestRunResumeAfterInterruption(t *testing.T) {
	inputs := buildInput(t)
	dir := t.TempDir()

	// First run completes fully.
	res1, err := Run(inputs, testCfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Re-run against the same WAL directory: committed stages are reused and
	// recovery must validate against the unchanged input digest.
	res2, err := Run(inputs, testCfg(dir))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res2.ManifestSHA256 != res1.ManifestSHA256 {
		t.Fatalf("resumed output must match uninterrupted run: %q vs %q",
			res2.ManifestSHA256, res1.ManifestSHA256)
	}
	if res2.RecoveryMS < 0 || res2.RecoveryMS >= 5000 {
		t.Fatalf("recovery_ms out of acceptance range: %d", res2.RecoveryMS)
	}
	// Manifest file must exist in the work directory.
	if _, err := readFile(t, filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestRunRejectsChangedInputOnResume(t *testing.T) {
	inputs := buildInput(t)
	dir := t.TempDir()
	if _, err := Run(inputs, testCfg(dir)); err != nil {
		t.Fatal(err)
	}
	// Mutate the input bytes; recovery must refuse to reuse the old WAL.
	inputs[0].Data = append(append([]byte{}, inputs[0].Data...), 0xAA)
	_, err := Run(inputs, testCfg(dir))
	if err == nil {
		t.Fatal("changed input must be rejected on resume")
	}
}
