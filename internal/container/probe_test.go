package container_test

import (
	"encoding/binary"
	"errors"
	"testing"

	"replaycheck/internal/clock"
	"replaycheck/internal/container"
	"replaycheck/internal/fixture"
)

func TestProbeGeneratedMP4RetainsTimebasesAndDrift(t *testing.T) {
	data := fixture.BuildMP4(fixture.MP4Spec{
		MovieTimescale: 1000,
		MovieDuration:  2000,
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Timescale: 90000, Duration: 180045, Deltas: repeat(60, 3000)},
			{TrackID: 2, Timescale: 48000, Duration: 96024, Deltas: []uint32{48000, 48000}},
		},
	})
	rep, anomalies, err := container.ProbeBytes("normal.mp4", data)
	if err != nil {
		t.Fatalf("ProbeBytes returned error: %v", err)
	}
	if len(rep.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(rep.Tracks))
	}
	if rep.Tracks[0].Timebase.Timescale != 90000 {
		t.Fatalf("timescale = %d", rep.Tracks[0].Timebase.Timescale)
	}
	if got := rep.Tracks[0].Timebase.DurationNS(); got != 2_000_500_000 {
		t.Fatalf("track duration ns = %d, want 2000500000", got)
	}
	if rep.Tracks[0].DriftNS != 500_000 {
		t.Fatalf("drift = %d ns, want 500000", rep.Tracks[0].DriftNS)
	}
	if rep.Tracks[0].DriftExceeded {
		t.Fatalf("0.5 ms drift must not exceed 2 ms")
	}
	if len(anomalies) != 0 {
		t.Fatalf("unexpected anomalies: %+v", anomalies)
	}
	if len(rep.Tracks[0].Timeline.Frames) != 60 {
		t.Fatalf("frames = %d, want 60", len(rep.Tracks[0].Timeline.Frames))
	}
}

func TestProbeDetectsDriftOverTwoMilliseconds(t *testing.T) {
	data := fixture.BuildMP4(fixture.MP4Spec{
		MovieTimescale: 1000,
		MovieDuration:  1000,
		Tracks: []fixture.TrackSpec{{
			TrackID: 1, Timescale: 90000, Duration: 90000 + 900, // 10 ms drift
			Deltas: repeat(30, 3000),
		}},
	})
	_, anomalies, err := container.ProbeBytes("drift.mp4", data)
	if err != nil {
		t.Fatalf("ProbeBytes: %v", err)
	}
	if len(anomalies) != 1 || anomalies[0].Kind != clock.CodeDrift {
		t.Fatalf("anomalies = %+v, want one drift anomaly", anomalies)
	}
}

func TestProbeTruncatedHeaderHasNonNegativeOffsetAndNoIndex(t *testing.T) {
	data := fixture.BuildMP4(fixture.MP4Spec{
		MovieTimescale: 1000, MovieDuration: 100,
		Tracks: []fixture.TrackSpec{{TrackID: 1, Timescale: 90000, Duration: 9000, Deltas: repeat(3, 3000)}},
	})[:40]
	rep, _, err := container.ProbeBytes("truncated.mp4", data)
	if err == nil {
		t.Fatalf("expected truncation error, got report %+v", rep)
	}
	var ce *clock.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error type %T, want *clock.Error", err)
	}
	if ce.Code != clock.CodeTruncated {
		t.Fatalf("code = %s, want %s", ce.Code, clock.CodeTruncated)
	}
	if ce.Offset < 0 {
		t.Fatalf("offset = %d, want non-negative", ce.Offset)
	}
}

func TestProbeMissingTrackTableReportsOffset(t *testing.T) {
	ftyp := boxForTest("ftyp", []byte("isom"+"\x00\x00\x02\x00"+"isommp42"))
	moov := boxForTest("moov", boxForTest("mvhd", make([]byte, 40)))
	data := append(ftyp, moov...)
	_, _, err := container.ProbeBytes("missing-trak.mp4", data)
	var ce *clock.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected clock.Error, got %T: %v", err, err)
	}
	if ce.Code != clock.CodeMissingTrack {
		t.Fatalf("code = %s, want %s", ce.Code, clock.CodeMissingTrack)
	}
	if ce.Offset < 0 {
		t.Fatalf("missing track offset = %d, want >= 0", ce.Offset)
	}
}

func boxForTest(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func repeat(n int, delta uint32) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = delta
	}
	return out
}
