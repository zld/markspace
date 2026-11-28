package validate

import (
	"os"
	"path/filepath"
	"testing"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/manifest"
	"rtp-rebuild/internal/mp4"
	"rtp-rebuild/internal/rtp"
)

func writeFixture(t *testing.T, n int) (string, *mp4.Track) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.mp4")
	pkts := make([]rtp.Packet, n)
	for i := range pkts {
		pkts[i] = rtp.Packet{TS: clock.Tick(i) * 3000, Payload: []byte("abc")}
	}
	track, err := mp4.WriteTrack(path, pkts, mp4.WriteOptions{})
	if err != nil {
		t.Fatalf("WriteTrack: %v", err)
	}
	return path, track
}

func baseInputs(t *testing.T, path string, samples int) Inputs {
	t.Helper()
	sha, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	mf := manifest.New(7, 10000, 0, []string{"ingest", "mux", "validate"})
	mfSHA, err := mf.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	return Inputs{
		ArtifactPath:        path,
		Manifest:            mf,
		ExpectedManifestSHA: mfSHA,
		ExpectedArtifactSHA: sha,
		ExpectedSamples:     samples,
		LossRate:            0,
		LossRateMax:         0.02,
		PeakBufferBytes:     1024,
		PeakPackets:         8,
		Budget: Budget{
			MaxBufferBytes: DefaultBufferBytes,
			MaxPackets:     DefaultMaxPackets,
			BudgetMS:       10000,
			ElapsedMS:      5,
		},
	}
}

func TestAllChecksPass(t *testing.T) {
	path, track := writeFixture(t, 10)
	in := baseInputs(t, path, len(track.Samples))
	rep, err := Run(in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Passed() {
		t.Fatalf("failures = %+v", rep.Failures())
	}
	passed, total := rep.Counts()
	if passed != total || total < 8 {
		t.Fatalf("counts = %d/%d", passed, total)
	}
}

func TestSampleCountMismatch(t *testing.T) {
	path, _ := writeFixture(t, 10)
	in := baseInputs(t, path, 11)
	rep, _ := Run(in)
	if rep.Passed() {
		t.Fatal("sample count mismatch should fail")
	}
	found := false
	for _, f := range rep.Failures() {
		if f.Name == "mp4_sample_count" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures = %+v", rep.Failures())
	}
}

func TestArtifactHashMismatch(t *testing.T) {
	path, track := writeFixture(t, 5)
	in := baseInputs(t, path, len(track.Samples))
	in.ExpectedArtifactSHA = "deadbeef"
	rep, _ := Run(in)
	if rep.Passed() {
		t.Fatal("artifact hash mismatch should fail")
	}
}

func TestManifestHashMismatch(t *testing.T) {
	path, track := writeFixture(t, 5)
	in := baseInputs(t, path, len(track.Samples))
	in.ExpectedManifestSHA = "deadbeef"
	rep, _ := Run(in)
	if rep.Passed() {
		t.Fatal("manifest hash mismatch should fail")
	}
}

func TestMemoryCeiling(t *testing.T) {
	path, track := writeFixture(t, 5)
	in := baseInputs(t, path, len(track.Samples))
	in.PeakBufferBytes = DefaultBufferBytes + 1
	rep, _ := Run(in)
	if rep.Passed() {
		t.Fatal("memory overrun should fail")
	}
	in = baseInputs(t, path, len(track.Samples))
	in.PeakPackets = DefaultMaxPackets + 1
	rep, _ = Run(in)
	if rep.Passed() {
		t.Fatal("packet ceiling overrun should fail")
	}
}

func TestBudgetDelta(t *testing.T) {
	path, track := writeFixture(t, 5)
	in := baseInputs(t, path, len(track.Samples))
	in.Budget.ElapsedMS = 10001
	rep, _ := Run(in)
	if rep.Passed() {
		t.Fatal("budget overrun should fail")
	}
	for _, f := range rep.Failures() {
		if f.Name == "time_budget" {
			if f.Detail != "elapsed_ms=10001 budget_ms=10000 delta_ms=1" {
				t.Fatalf("detail = %q", f.Detail)
			}
			return
		}
	}
	t.Fatal("time_budget failure not reported")
}

func TestLossRateThreshold(t *testing.T) {
	path, track := writeFixture(t, 5)
	in := baseInputs(t, path, len(track.Samples))
	in.LossRate = 0.02
	rep, _ := Run(in)
	if !rep.Passed() {
		t.Fatal("exactly 2% should pass (threshold is exceeded, not reached)")
	}
	in.LossRate = 0.0201
	rep, _ = Run(in)
	if rep.Passed() {
		t.Fatal("above 2% should fail")
	}
}

func TestGapIntervalFails(t *testing.T) {
	path, track := writeFixture(t, 5)
	in := baseInputs(t, path, len(track.Samples))
	in.Gaps = 1
	rep, _ := Run(in)
	if rep.Passed() {
		t.Fatal("any gap interval should fail")
	}
}

func TestMissingArtifact(t *testing.T) {
	path, track := writeFixture(t, 3)
	in := baseInputs(t, path, len(track.Samples))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(in)
	if err == nil {
		t.Fatal("missing artifact should be an error")
	}
	if rep.Passed() {
		t.Fatal("missing artifact should not pass")
	}
	if len(rep.Failures()) != 1 || rep.Failures()[0].Name != "mp4_parse" {
		t.Fatalf("failures = %+v", rep.Failures())
	}
}

func TestProbeRejectsNonMP4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.mp4")
	if err := os.WriteFile(path, []byte("not an mp4 file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeMP4(path); err == nil {
		t.Fatal("junk should not parse")
	}
}

func TestSortedArtifacts(t *testing.T) {
	m := manifest.New(1, 1, 0, nil)
	m.AddArtifact("z", "1", 1)
	m.AddArtifact("a", "2", 2)
	got := SortedArtifacts(m)
	if got[0].Name != "a" || got[1].Name != "z" {
		t.Fatalf("sorted = %+v", got)
	}
}
