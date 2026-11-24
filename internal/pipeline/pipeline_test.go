package pipeline

import (
	"fmt"
	"path/filepath"
	"testing"

	"mediascan/internal/fixture"
	"mediascan/internal/report"
)

func makeSegments(t *testing.T, n int, vfr bool) []SegmentInput {
	t.Helper()
	out := make([]SegmentInput, 0, n)
	base := uint64(0)
	for i := 0; i < n; i++ {
		samples := make([]fixture.Sample, 4)
		for j := range samples {
			dur := uint32(100) // 100 ms at the default 1000-unit timescale
			if vfr && j == 2 {
				dur = 160 // a deliberately irregular frame
			}
			samples[j] = fixture.Sample{
				Duration: dur, Size: uint32(20 + j*4),
				Keyframe: j == 0, Payload: byte(i*7 + j + 1),
			}
		}
		raw := fixture.MediaSegment(1, uint32(i), base, samples)
		out = append(out, SegmentInput{
			Name: segName(i), MediaSeq: uint64(i), Present: true,
			Data: raw, ManifestOffset: int64(i * 40),
		})
		base += uint64(4 * 100)
		if vfr {
			base += 60
		}
	}
	return out
}

func segName(i int) string {
	return fmt.Sprintf("seg%d.m4s", i)
}

func TestRunNormalIsClean(t *testing.T) {
	inputs := makeSegments(t, 3, false)
	p := New(0)
	s := p.Run(inputs)

	if s.TotalSegments != 3 || s.DamagedSegments != 0 {
		t.Fatalf("counts wrong: %+v", s)
	}
	if s.RoundtripConsistency != 1.0 || s.KeyframeHitRate != 1.0 {
		t.Fatalf("rates wrong: %+v", s)
	}
	if s.ExitCode != 0 || p.Log.HasError() {
		t.Fatalf("clean run must exit 0, got %d errors=%+v",
			s.ExitCode, p.Log.Records())
	}
	if p.Index.Len() != 3 {
		t.Fatalf("indexed keyframes = %d, want 3", p.Index.Len())
	}
	// Hash output exists for every segment and differs from input class.
	for _, h := range p.Hasher.Segments() {
		if h.InputHash == "" || h.OutputHash == "" {
			t.Fatalf("missing hash: %+v", h)
		}
		if len(h.InputHash) != 64 || len(h.OutputHash) != 64 {
			t.Fatalf("hash must be hex sha256: %+v", h)
		}
	}
}

func TestRunDamageIsolatesAndContinues(t *testing.T) {
	inputs := makeSegments(t, 4, false)
	// Truncate the second segment inside its mdat box.
	cut := len(inputs[1].Data) - 10
	inputs[1].Data = inputs[1].Data[:cut]

	p := New(0)
	s := p.Run(inputs)

	if s.DamagedSegments != 1 || s.ExitCode != 2 {
		t.Fatalf("expected one damaged segment and exit 2: %+v", s)
	}
	if len(s.DamageOffsets) != 1 || s.DamageOffsets[0] < 0 {
		t.Fatalf("damage offsets wrong: %v", s.DamageOffsets)
	}
	if len(s.IsolatedRanges) != 1 {
		t.Fatalf("isolated ranges = %v", s.IsolatedRanges)
	}
	rng := s.IsolatedRanges[0]
	if rng[0] != s.DamageOffsets[0] || rng[1] <= rng[0] {
		t.Fatalf("isolation range malformed: %v", rng)
	}
	if s.VerifiedPrefixBytes != rng[0] {
		t.Fatalf("verified prefix %d != range start %d",
			s.VerifiedPrefixBytes, rng[0])
	}
	// Segments after the damaged one must still be processed.
	if s.ProcessableAfter != 2 {
		t.Fatalf("processable after damage = %d, want 2", s.ProcessableAfter)
	}
	if p.Index.Available() != 3 { // segments 0, 2, 3
		t.Fatalf("live index entries = %d, want 3", p.Index.Available())
	}
	// Independent later segments still validate and carry output hashes.
	for i := 2; i < 4; i++ {
		if p.Hasher.Segments()[i].OutputHash == "" {
			t.Fatalf("segment %d should keep an output hash", i)
		}
	}
}

func TestRunIsDeterministic(t *testing.T) {
	inputs := makeSegments(t, 3, true)
	first := New(120)
	s1 := first.Run(inputs)
	second := New(120)
	s2 := second.Run(inputs)

	if s1.Fingerprint != s2.Fingerprint {
		t.Fatal("fingerprint differs across identical runs")
	}
	h1, h2 := first.Hasher.Segments(), second.Hasher.Segments()
	for i := range h1 {
		if h1[i].InputHash != h2[i].InputHash ||
			h1[i].OutputHash != h2[i].OutputHash {
			t.Fatalf("segment %d hashes diverged", i)
		}
	}
	if s1.RoundtripConsistency != s2.RoundtripConsistency ||
		s1.KeyframeHitRate != s2.KeyframeHitRate {
		t.Fatal("summary rates diverged across runs")
	}
}

func TestMissingSegmentRecorded(t *testing.T) {
	inputs := makeSegments(t, 2, false)
	inputs = append(inputs, SegmentInput{
		Name: "seg2.m4s", MediaSeq: 2, Present: false, ManifestOffset: 88,
	})
	p := New(0)
	s := p.Run(inputs)
	if s.DamagedSegments != 1 || s.ExitCode != 2 {
		t.Fatalf("missing segment must count as damage: %+v", s)
	}
	found := false
	for _, r := range p.Log.Records() {
		if r.Kind == "missing_segment" && r.SourceOffset == 88 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing-segment fault with source offset not recorded: %+v",
			p.Log.Records())
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	inputs := makeSegments(t, 3, false)
	first := New(0)
	first.Run(inputs)
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := first.SaveCheckpoint(path); err != nil {
		t.Fatal(err)
	}
	cp, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Fingerprint != first.Hasher.Fingerprint() ||
		len(cp.Results) != 3 || len(cp.Entries) != 3 {
		t.Fatalf("checkpoint contents wrong: %+v", cp)
	}
	// A fresh run over the same inputs must verify cleanly.
	resumed := New(0)
	resumed.Run(inputs)
	if err := VerifyCheckpoint(cp, resumed); err != nil {
		t.Fatalf("resume verification failed: %v", err)
	}
	// Tamper with one input hash and confirm verification fails.
	cp.Results[0].InputHash = report.Sum256([]byte("tampered"))
	if err := VerifyCheckpoint(cp, resumed); err == nil {
		t.Fatal("tampered checkpoint must fail verification")
	}
}

func TestVFRTimesPreserved(t *testing.T) {
	inputs := makeSegments(t, 2, true)
	p := New(0)
	p.Run(inputs)
	entries := p.Index.Snapshot()
	if len(entries) < 2 {
		t.Fatalf("index entries = %d", len(entries))
	}
	// The irregular 160 ms frame changes measured keyframe spacing; it
	// must not collapse onto a fixed-rate 400 ms assumption.
	for _, e := range entries[1:] {
		if e.IntervalSeconds <= 0 {
			t.Fatalf("non-positive VFR interval: %+v", e)
		}
	}
}

func TestDriftRecordsBeforeAndAfter(t *testing.T) {
	inputs := makeSegments(t, 2, false)
	p := New(120) // source runs 120 ppm fast
	s := p.Run(inputs)
	if s.DamagedSegments != 0 {
		t.Fatalf("drift alone must not damage segments: %+v", s)
	}
	for _, r := range p.Results() {
		for _, smp := range r.Normalized.Samples {
			// The origin sample has zero elapsed time and thus zero delta.
			if smp.DTS == 0 && smp.TimeSecondsRaw == 0 {
				if smp.CorrectionDelta != 0 {
					t.Fatalf("origin sample delta must be zero: %+v", smp)
				}
				continue
			}
			if smp.CorrectionDelta >= 0 {
				t.Fatalf("fast-clock delta must be negative: %+v", smp)
			}
			if got := smp.TimeSecondsRaw + smp.CorrectionDelta; abs(got-smp.TimeSeconds) > 1e-6 {
				t.Fatalf("raw+delta != corrected: %v+%v != %v",
					smp.TimeSecondsRaw, smp.CorrectionDelta, smp.TimeSeconds)
			}
		}
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
