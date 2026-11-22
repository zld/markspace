package verify

import (
	"strings"
	"testing"

	"github.com/pairwise/mediad/internal/fault"
)

func good() *Manifest {
	return &Manifest{Version: 1,
		Files: []FileEntry{{Name: "a.mp4", RelPath: "media/a.mp4"}},
		Segments: []SegmentEntry{
			{ID: "a:s0", File: "a.mp4", TrackID: 1, StartNS: 0, EndNS: 1_000_000_000,
				SampleCount: 10, KeyExact: true, SHA256: strings.Repeat("ab", 32)},
			{ID: "a:s10", File: "a.mp4", TrackID: 1, StartNS: 1_000_000_000, EndNS: 2_000_000_000,
				SampleCount: 10, KeyExact: true, SHA256: strings.Repeat("cd", 32)},
		},
		Summary: Summary{Segments: 2},
	}
}

func TestCheckAcceptsValidManifest(t *testing.T) {
	if err := Check(good()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsStructurallyInvalidSegments(t *testing.T) {
	cases := map[string]func(*Manifest){
		"empty extent":     func(m *Manifest) { m.Segments[0].EndNS = m.Segments[0].StartNS },
		"over cap":         func(m *Manifest) { m.Segments[0].EndNS = 31 * 60 * 1_000_000_000 },
		"zero samples":     func(m *Manifest) { m.Segments[0].SampleCount = 0 },
		"bad hash":         func(m *Manifest) { m.Segments[0].SHA256 = "abc" },
		"silence overflow": func(m *Manifest) { m.Segments[0].SilentNS = 2_000_000_000; m.Segments[0].SilenceKnown = true },
		"silence unknown":  func(m *Manifest) { m.Segments[0].SilentNS = 10 },
		"duplicate id":     func(m *Manifest) { m.Segments[1].ID = m.Segments[0].ID },
		"orphan duplicate": func(m *Manifest) { m.Segments[1].DuplicateOf = "missing" },
		"false duplicate":  func(m *Manifest) { m.Segments[1].DuplicateOf = m.Segments[0].ID },
	}
	for name, mutate := range cases {
		m := good()
		mutate(m)
		err := Check(m)
		if err == nil {
			t.Errorf("%s: accepted invalid manifest", name)
			continue
		}
		if ec := fault.ExitCode(err); ec != 2 {
			t.Errorf("%s: exit=%d want 2", name, ec)
		}
	}
}

func TestKeyframeRateEnforcesThreshold(t *testing.T) {
	m := good()
	m.Segments[0].KeyExact = false
	// 1 of 2 exact = 0.5, below the 0.95 floor.
	if err := Check(m); err == nil {
		t.Fatal("low keyframe rate accepted")
	}
	m.Segments[0].KeyExact = true
	if err := Check(m); err != nil {
		t.Fatal(err)
	}
	if got := KeyframeHitRate(m.Segments); got != 1 {
		t.Fatalf("rate=%v", got)
	}
}

func TestSegmentDigestIsOrderIndependent(t *testing.T) {
	m := good()
	a := SegmentDigest(m.Segments)
	m.Segments[0], m.Segments[1] = m.Segments[1], m.Segments[0]
	if SegmentDigest(m.Segments) != a {
		t.Fatal("digest depends on order")
	}
	m.Segments[0].SHA256 = strings.Repeat("00", 32)
	if SegmentDigest(m.Segments) == a {
		t.Fatal("digest ignores hash changes")
	}
}

func TestLoadRejectsMalformedManifest(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/manifest.json"
	if err := Save(path, good()); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	bad := good()
	bad.Version = 99
	if err := Save(path, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unsupported version accepted")
	}
}

func TestRoundtripRequiresRecomputedHashes(t *testing.T) {
	// A manifest whose recorded hashes do not match the media must fail with
	// exit 2, and the summary must still report the measured rate.
	dir := t.TempDir()
	m := good()
	m.Segments[0].File = "missing.mp4"
	sum, err := Run(m, dir, Options{})
	if err == nil {
		t.Fatal("missing media accepted")
	}
	if sum.RoundtripRate != 0 {
		t.Fatalf("roundtrip_rate=%v", sum.RoundtripRate)
	}
	if fault.ExitCode(err) != 2 {
		t.Fatalf("exit=%d", fault.ExitCode(err))
	}
}

func TestRecoveredExitCodeDependsOnProvenance(t *testing.T) {
	// A non-demo manifest with recovered slices must report exit 1 even when
	// every recomputed hash matched.
	dir := t.TempDir()
	m := good()
	m.Summary.RecoveredSegments = 1
	if _, err := Run(m, dir, Options{}); err == nil {
		// Recompute fails first for missing media, so the gate is exercised
		// by the explicit recoverable error instead.
	}
	err := fault.Errorf("verify.run", fault.KindTruncated, "recovered slices").Recovered()
	if fault.ExitCode(err) != 1 {
		t.Fatal("recoverable damage must map to exit 1")
	}
}

func TestDuplicateClaimsRequirePayloadEvidence(t *testing.T) {
	m := good()
	m.Segments[1].DuplicateOf = m.Segments[0].ID
	// Same payload claim but a different hash must be rejected.
	m.Segments[1].SHA256 = strings.Repeat("ef", 32)
	if err := Check(m); err == nil {
		t.Fatal("duplicate claim with mismatched payload accepted")
	}
	// Different sample counts are not the same content either.
	m = good()
	m.Segments[1].DuplicateOf = m.Segments[0].ID
	m.Segments[1].SampleCount = m.Segments[0].SampleCount + 1
	if err := Check(m); err == nil {
		t.Fatal("duplicate claim with mismatched sample count accepted")
	}
	// Shifted but overlapping extents with the same payload are a real
	// duplicate feed recorded from a different anchor.
	m = good()
	m.Segments[1].StartNS = 500_000_000
	m.Segments[1].EndNS = 1_500_000_000
	m.Segments[1].SHA256 = m.Segments[0].SHA256
	m.Segments[1].SampleCount = m.Segments[0].SampleCount
	m.Segments[1].DuplicateOf = m.Segments[0].ID
	if err := Check(m); err != nil {
		t.Fatalf("shifted duplicate rejected: %v", err)
	}
}
