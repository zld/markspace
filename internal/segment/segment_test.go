package segment

import (
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/probe"
)

func recoveredTwoTracks() map[uint32][]probe.RecoveredSample {
	mk := func(id uint32, count int, startStep clock.Tick) []probe.RecoveredSample {
		out := make([]probe.RecoveredSample, count)
		for i := range out {
			out[i] = probe.RecoveredSample{
				TrackID: id, Index: uint32(i), Offset: clock.Offset(int(id)*1000 + i*32),
				Size: 32, Start: clock.Tick(i) * startStep, Duration: startStep,
				Sync: i%4 == 0,
			}
		}
		return out
	}
	return map[uint32][]probe.RecoveredSample{
		1: mk(1, 12, 900),
		2: mk(2, 12, 900),
	}
}

func TestAlignProducesSynchronizedBoundaries(t *testing.T) {
	al, err := Align(recoveredTwoTracks())
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	if len(al.Boundaries) != 3 {
		t.Fatalf("boundaries = %d, want 3", len(al.Boundaries))
	}
	for _, b := range al.Boundaries {
		if b.MaxDelta > MaxBoundaryDelta {
			t.Fatalf("delta %d exceeds limit", b.MaxDelta)
		}
		if _, ok := b.MemberStarts[1]; !ok {
			t.Fatal("reference member missing")
		}
		if _, ok := b.MemberStarts[2]; !ok {
			t.Fatal("second representation missing from group")
		}
	}
}

func TestAlignFailsWhenBoundaryDiverges(t *testing.T) {
	rec := recoveredTwoTracks()
	// Shift track 2's sync sample so the difference exceeds one tick.
	rec[2][4].Start += 5
	_, err := Align(rec)
	if err == nil {
		t.Fatal("expected alignment failure for divergent boundary")
	}
}

func TestAlignFailsOnMissingRepresentationBoundary(t *testing.T) {
	rec := recoveredTwoTracks()
	// Drop every sync sample of track 2 after the first group.
	for i := range rec[2] {
		if i >= 4 {
			rec[2][i].Sync = false
		}
	}
	if _, err := Align(rec); err == nil {
		t.Fatal("expected failure when a representation lacks a boundary")
	}
}

func TestManifestIsDeterministicAndVerifies(t *testing.T) {
	rec := recoveredTwoTracks()
	al, err := Align(rec)
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	input := []byte("deterministic-input-bytes")
	digest := DigestInput(input)
	m1, err := Build(al, rec, digest)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	m2, err := Build(al, rec, digest)
	if err != nil {
		t.Fatalf("Build rerun: %v", err)
	}
	if d := DiffCount(m1, m2); d != 0 {
		t.Fatalf("rerun diff = %d, want 0", d)
	}
	if m1.SegmentDigest != m2.SegmentDigest {
		t.Fatal("segment digest changed across reruns")
	}
	raw1, _ := m1.Encode()
	raw2, _ := m2.Encode()
	if string(raw1) != string(raw2) {
		t.Fatal("canonical JSON not byte-stable")
	}
	report := Verify(m1, input, []uint32{1, 2})
	if !report.OK() {
		t.Fatalf("verification failed: %v", report.Errors)
	}
	if len(m1.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(m1.Segments))
	}
}

func TestVerifyDetectsTamperedDigest(t *testing.T) {
	rec := recoveredTwoTracks()
	al, _ := Align(rec)
	input := []byte("abc")
	m, _ := Build(al, rec, DigestInput(input))
	tampered := []byte("abd")
	report := Verify(m, tampered, []uint32{1, 2})
	if report.FileDigestOK {
		t.Fatal("tampered input must fail file digest check")
	}
	if report.OK() {
		t.Fatal("report must not be OK after tampering")
	}
}

func TestVerifyDetectsMissingRepresentation(t *testing.T) {
	rec := recoveredTwoTracks()
	al, _ := Align(rec)
	m, _ := Build(al, rec, DigestInput([]byte("abc")))
	report := Verify(m, []byte("abc"), []uint32{1, 2, 3})
	if report.RepresentationSetOK {
		t.Fatal("missing representation not detected")
	}
}
