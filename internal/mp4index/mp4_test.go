package mp4index

import (
	"testing"

	"tsrepair/internal/mediaerr"
	"tsrepair/internal/segment"
)

func testManifest() *Manifest {
	return &Manifest{
		Representation: 2,
		Timescale:      Timescale,
		Duration90k:    270_000,
		// VFR spacing: deliberately non-uniform deltas.
		Samples: []SampleRecord{
			{Delta90k: 3000, Size: 100},
			{Delta90k: 6000, Size: 200},
			{Delta90k: 3000, Size: 150},
		},
		KeyframeNumbers:  []uint32{1, 3},
		SegmentCanonical: [][]byte{segment.Canonical(&segment.Segment{ID: 0, SampleCount: 3})},
	}
}

func TestSerializeParseRoundtrip(t *testing.T) {
	if err := RoundTrip(testManifest()); err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestParsePreservesVFRAndKeyframes(t *testing.T) {
	wire, err := Serialize(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	got, mdat, err := Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timescale != Timescale || got.Duration90k != 270_000 {
		t.Errorf("header wrong: ts=%d dur=%d", got.Timescale, got.Duration90k)
	}
	if len(got.Samples) != 3 || got.Samples[1].Delta90k != 6000 || got.Samples[1].Size != 200 {
		t.Errorf("VFR samples not preserved: %+v", got.Samples)
	}
	if len(got.KeyframeNumbers) != 2 || got.KeyframeNumbers[1] != 3 {
		t.Errorf("keyframes not preserved: %v", got.KeyframeNumbers)
	}
	if len(mdat) == 0 {
		t.Error("mdat payload lost")
	}
}

func TestRoundTripDetectsFieldMismatch(t *testing.T) {
	good := testManifest()
	wire, err := Serialize(good)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the parsed view and confirm the comparator flags the field.
	parsed.Samples[0].Delta90k = 9999
	if err := compare(good, parsed, segment.Canonical(&segment.Segment{ID: 0, SampleCount: 3})); err == nil {
		t.Fatal("expected round-trip mismatch error")
	} else {
		me, ok := err.(*mediaerr.Error)
		if !ok || me.Code != mediaerr.CodeRoundtripField {
			t.Fatalf("wrong error: %v", err)
		}
	}
}

func TestRoundTripDetectsCountMismatch(t *testing.T) {
	good := testManifest()
	parsed, mdat, err := Parse(mustSerialize(t, good))
	if err != nil {
		t.Fatal(err)
	}
	parsed.KeyframeNumbers = parsed.KeyframeNumbers[:1] // drop a keyframe
	if err := compare(good, parsed, mdat); err == nil {
		t.Fatal("expected stss count mismatch")
	}
}

func TestRoundTripDetectsMDATChange(t *testing.T) {
	good := testManifest()
	if err := RoundTrip(good); err != nil {
		t.Fatal(err)
	}
	// Different canonical segment content must change the mdat comparison.
	other := testManifest()
	other.SegmentCanonical = [][]byte{segment.Canonical(&segment.Segment{ID: 9, SampleCount: 3})}
	wire, err := Serialize(other)
	if err != nil {
		t.Fatal(err)
	}
	parsed, mdat, err := Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := compare(good, parsed, mdat); err == nil {
		t.Fatal("expected mdat payload mismatch")
	}
}

func mustSerialize(t *testing.T, m *Manifest) []byte {
	t.Helper()
	b, err := Serialize(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
