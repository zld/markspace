package clock

import (
	"testing"

	"mediarc/internal/model"
)

func makeTrack(id uint32, dts []uint64, dur uint32, clock bool) model.Track {
	tr := model.Track{ID: id, Codec: model.CodecAVC1, Timescale: 1000}
	for i, d := range dts {
		tr.Samples = append(tr.Samples, model.Sample{Dts: d, Duration: dur, Payload: []byte{byte(i)}, Sync: i == 0})
	}
	if clock {
		// Mirror each sample as a minimal RTP packet on a 90 kHz clock.
		tr.ClockPackets = buildClockPackets(dts, dur)
	}
	return tr
}

func TestRecoverRepresentationWraparoundAndRawPreserved(t *testing.T) {
	rep := model.Representation{ID: "r1", Format: "dash", Timescale: 1000}
	// Two samples on each side of a 32-bit wrap, constant 10-tick duration.
	dts := []uint64{0xFFFFFFF8, 0x00000002, 0x0000000C, 0x00000016}
	tracks := []model.Track{makeTrack(1, dts, 10, true)}
	out := RecoverRepresentation(rep, tracks, DefaultFanoutConfig())
	if !out.OK {
		t.Fatalf("expected OK, errors=%v", out.Errors)
	}
	rt := out.Tracks[0]
	if rt.WrapCount != 1 {
		t.Fatalf("WrapCount=%d want 1", rt.WrapCount)
	}
	// Original raw timestamps must survive untouched.
	for i, d := range dts {
		if rt.RawTimes[i] != d {
			t.Fatalf("RawTimes[%d]=%d want %d", i, rt.RawTimes[i], d)
		}
	}
	if rt.Unwrapped[2] <= rt.Unwrapped[1] {
		t.Fatalf("unwrapped timeline not monotonic: %v", rt.Unwrapped)
	}
	if len(rt.Boundaries) == 0 || rt.Boundaries[0].StartMs != rt.Boundaries[0].StartMs {
		t.Fatal("boundaries missing")
	}
}

func TestRecoverRepresentationDriftExceeded(t *testing.T) {
	rep := model.Representation{ID: "r-drift", Format: "dash"}
	// Timestamps run 1% faster than the declared 1000-tick duration.
	dts := make([]uint64, 12)
	for i := 1; i < len(dts); i++ {
		dts[i] = dts[i-1] + 1010
	}
	tracks := []model.Track{makeTrack(7, dts, 1000, false)}
	cfg := FanoutConfig{MaxSamplesPerTrack: 100000, DriftThresholdMsPerSec: 5}
	out := RecoverRepresentation(rep, tracks, cfg)
	if out.OK {
		t.Fatal("drift over threshold must make the representation not-OK")
	}
	found := false
	for _, e := range out.Errors {
		if e.Kind == model.KindDriftExceeded && e.Item == "r-drift" {
			found = true
		}
	}
	if !found {
		t.Fatalf("drift error missing: %v", out.Errors)
	}
	if out.Tracks[0].DriftPPM < 9900 || out.Tracks[0].DriftPPM > 10100 {
		t.Fatalf("DriftPPM=%d want ~10000", out.Tracks[0].DriftPPM)
	}
}

func TestRecoverRepresentationIsolation(t *testing.T) {
	rep := model.Representation{ID: "r-bad"}
	good := makeTrack(1, []uint64{0, 10, 20}, 10, false)
	empty := model.Track{ID: 2, Codec: model.CodecMP4A, Timescale: 1000}
	out := RecoverRepresentation(rep, []model.Track{good, empty}, DefaultFanoutConfig())
	if out.OK {
		t.Fatal("expected failure from the empty track")
	}
	if len(out.Tracks) != 2 || !out.Tracks[0].Trusted {
		t.Fatal("healthy track recovery must be preserved beside the corrupt one")
	}
	if out.Tracks[1].Trusted {
		t.Fatal("empty track must be untrusted")
	}
}
