package batch

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"loudcheck/internal/fixture"
	"loudcheck/internal/policy"
)

// writeTone writes a synthetic WAV file of the given layout and returns its
// path. The caller supplies the directory so several files can share one.
func writeTone(t *testing.T, dir, name string, channels int, dbfs float64, seconds float64, enc fixture.Encoding) string {
	t.Helper()
	opts := fixture.Options{
		SampleRate: 48000, Channels: channels, Seconds: seconds,
		Encoding: enc, ToneDBFS: dbfs, ToneHz: 997, Extensible: true,
	}
	sig := fixture.Signal(opts)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, fixture.Encode(opts, sig), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestAnalyzeFilesSortsAndPasses(t *testing.T) {
	dir := t.TempDir()
	// Deliberately create files out of lexical order. A -20 dBFS stereo tone
	// measures about -20.0 LUFS, so a target of -20 with 1 LU tolerance is a
	// comfortable pass for both.
	a := writeTone(t, dir, "a.wav", 2, -20, 4, fixture.PCM16)
	b := writeTone(t, dir, "b.wav", 2, -20, 4, fixture.PCM24)
	th := policy.Thresholds{TargetLUFS: -20, ToleranceLU: 1, MaxTruePeakDBTP: -1}

	summary := AnalyzeFiles([]string{b, a}, Options{Thresholds: th, Workers: 2})
	if len(summary.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(summary.Results))
	}
	if summary.Results[0].Path != a || summary.Results[1].Path != b {
		t.Fatalf("results are not sorted by path: %s, %s", summary.Results[0].Path, summary.Results[1].Path)
	}
	if summary.Results[0].Err != "" || summary.Results[1].Err != "" {
		t.Fatalf("unexpected errors: %q %q", summary.Results[0].Err, summary.Results[1].Err)
	}
	if !summary.AllPassed() {
		t.Errorf("both tones should pass: passed=%d not=%d failed=%d",
			summary.Passed, summary.NotPassed, summary.Failed)
	}
}

func TestWorkerCountDoesNotChangeResults(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i, ch := range []int{1, 2, 6, 8} {
		name := []string{"mono", "stereo", "surround51", "surround71"}[i] + ".wav"
		paths = append(paths, writeTone(t, dir, name, ch, -23, 4, fixture.Float32))
	}
	th := policy.Thresholds{TargetLUFS: -21, ToleranceLU: 3, MaxTruePeakDBTP: -1}

	one := AnalyzeFiles(paths, Options{Thresholds: th, Workers: 1})
	many := AnalyzeFiles(paths, Options{Thresholds: th, Workers: 4})
	if len(one.Results) != len(many.Results) {
		t.Fatal("worker count changed the number of results")
	}
	for i := range one.Results {
		a, b := one.Results[i], many.Results[i]
		if a.Path != b.Path {
			t.Fatalf("order differs at %d: %s vs %s", i, a.Path, b.Path)
		}
		if a.Measure.IntegratedLUFS != b.Measure.IntegratedLUFS ||
			a.Measure.TruePeak != b.Measure.TruePeak ||
			a.Verdict.Pass != b.Verdict.Pass {
			t.Errorf("%s: worker count changed the measurement (%+v vs %+v)", a.Path, a.Measure, b.Measure)
		}
	}
}

func TestCorruptFileDoesNotHideOtherResults(t *testing.T) {
	dir := t.TempDir()
	good := writeTone(t, dir, "good.wav", 1, -23, 4, fixture.PCM16)
	bad := filepath.Join(dir, "bad.wav")
	if err := os.WriteFile(bad, []byte("RIFF\x24\x00\x00\x00WAVEfmt "), 0o600); err != nil {
		t.Fatal(err)
	}
	// A mono -23 dBFS tone measures about -26 LUFS; match the target so the
	// only failure in this batch is the corrupt file.
	th := policy.Thresholds{TargetLUFS: -26, ToleranceLU: 1, MaxTruePeakDBTP: -1}
	summary := AnalyzeFiles([]string{good, bad}, Options{Thresholds: th, Workers: 2})

	if len(summary.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(summary.Results))
	}
	if summary.Failed != 1 {
		t.Errorf("failed = %d, want 1", summary.Failed)
	}
	if summary.Passed != 1 {
		t.Errorf("passed = %d, want 1", summary.Passed)
	}
	if summary.AllPassed() {
		t.Error("a batch with a corrupt file must not report all-pass")
	}
	for _, r := range summary.Results {
		if r.Path == bad && r.Err == "" {
			t.Error("corrupt file has no recorded error")
		}
		if r.Path == good && (r.Err != "" || !r.Verdict.Pass) {
			t.Errorf("good file was affected: %+v", r)
		}
	}
}

func TestUnknownLayoutIsPerFileError(t *testing.T) {
	dir := t.TempDir()
	// Three channels has no default layout, so analysis must refuse it while
	// leaving the rest of the batch intact.
	odd := writeTone(t, dir, "three.wav", 3, -23, 4, fixture.PCM24)
	ok := writeTone(t, dir, "stereo.wav", 2, -23, 4, fixture.PCM24)
	// Three equally weighted channels sum to +4.77 LU over mono, so a wide
	// tolerance keeps this test about the layout error and not about level.
	th := policy.Thresholds{TargetLUFS: -21.2, ToleranceLU: 2, MaxTruePeakDBTP: -1}
	summary := AnalyzeFiles([]string{odd, ok}, Options{Thresholds: th, Workers: 2})
	if summary.Failed != 1 {
		t.Fatalf("failed = %d, want 1 (errors: %q %q)", summary.Failed, summary.Results[0].Err, summary.Results[1].Err)
	}

	// With an explicit map the same file measures.
	mapped := AnalyzeFiles([]string{odd}, Options{
		Thresholds: th, Workers: 1, Mapping: "1,1,1",
	})
	if mapped.Results[0].Err != "" {
		t.Fatalf("explicit mapping failed: %v", mapped.Results[0].Err)
	}
	if !mapped.Results[0].Verdict.Pass {
		t.Errorf("mapped 3-channel file failed: %+v", mapped.Results[0].Verdict)
	}
}

func TestExpandPathDirectory(t *testing.T) {
	dir := t.TempDir()
	writeTone(t, dir, "z.wav", 1, -23, 1, fixture.PCM16)
	writeTone(t, dir, "a.wav", 1, -23, 1, fixture.PCM16)
	writeTone(t, dir, "notes.txt", 1, -23, 1, fixture.PCM16)
	if err := os.Mkdir(filepath.Join(dir, "nested.wav"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ExpandPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.wav"), filepath.Join(dir, "z.wav")}
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %s, want %s", i, got[i], want[i])
		}
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
		t.Error("entries are not sorted")
	}
}

func TestExpandPathRejectsRemoteAndEmpty(t *testing.T) {
	if _, err := ExpandPath("https://example.com/a.wav"); err == nil {
		t.Error("URL input should be rejected")
	}
	if _, err := ExpandPath(t.TempDir()); err == nil {
		t.Error("a directory with no WAV files should be an error")
	}
}

func TestEmptyInputReturnsEmptySummary(t *testing.T) {
	s := AnalyzeFiles(nil, Options{Thresholds: policy.DefaultThresholds(), Workers: 4})
	if len(s.Results) != 0 || s.AllPassed() {
		t.Fatalf("empty input = %+v", s)
	}
}
