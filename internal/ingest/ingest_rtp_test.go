package ingest

import (
	"path/filepath"
	"testing"

	"mediapipe/internal/fault"
	"mediapipe/internal/testdata"
)

func writeRTPFixture(t *testing.T, dir string, spec testdata.RTPWrapSpec) string {
	t.Helper()
	pkts, late := testdata.BuildRTPPackets(spec)
	path := filepath.Join(dir, "capture.rtp")
	if err := testdata.WriteRTP(path, pkts, late); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRTPWrapAndDrift(t *testing.T) {
	dir := t.TempDir()
	path := writeRTPFixture(t, dir, testdata.RTPWrapSpec{
		ClockRate: 48000, PacketSteps: 960, StartTS: 1000,
		DriftPPM: 180, Packets: 60, Wrap: true,
	})
	var errs fault.Batch
	res, err := ReadRTPCapture(path, "rtp-0", 48000, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Wraps == 0 {
		t.Error("expected at least one timestamp wrap")
	}
	if !errs.HasKind(fault.KindTimestampWrap) {
		t.Error("wrap fault not reported with offset")
	}
	for _, e := range errs.All() {
		if e.Kind == fault.KindTimestampWrap && e.Offset < 0 {
			t.Error("wrap fault missing source offset")
		}
	}
	// Expanded timestamps must be monotonic despite the 32-bit rollover.
	prev := int64(-1)
	for _, s := range res.Track.Samples {
		if s.PTS < prev {
			t.Fatalf("non-monotonic expanded PTS: %d after %d", s.PTS, prev)
		}
		if s.DurationNanos < 0 {
			t.Fatal("negative duration after unwrap")
		}
		prev = s.PTS
	}
	// 180 PPM drift should be estimated within a loose tolerance band.
	if res.DriftPPM <= 0 || res.DriftPPM > 500 {
		t.Errorf("drift estimate = %.1f ppm, want within (0,500]", res.DriftPPM)
	}
}

func TestRTPLatePacketQuarantine(t *testing.T) {
	dir := t.TempDir()
	path := writeRTPFixture(t, dir, testdata.RTPWrapSpec{
		ClockRate: 48000, PacketSteps: 960, StartTS: 1000,
		DriftPPM: 0, Packets: 40, ReorderIndex: 5, LateIndex: 38,
	})
	var errs fault.Batch
	res, err := ReadRTPCapture(path, "rtp-0", 48000, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Quarantined == 0 {
		t.Fatal("late packet must be quarantined")
	}
	if !errs.HasKind(fault.KindLatePacket) {
		t.Error("late-packet fault not reported")
	}
	// Committed sequence must not be rewritten: samples stay ordered.
	for i := 1; i < len(res.Track.Samples); i++ {
		if res.Track.Samples[i].PTS < res.Track.Samples[i-1].PTS {
			t.Fatal("committed timeline was rewritten by late arrival")
		}
	}
}

func TestRTPTruncatedRecord(t *testing.T) {
	dir := t.TempDir()
	path := writeRTPFixture(t, dir, testdata.RTPWrapSpec{
		ClockRate: 48000, PacketSteps: 960, StartTS: 0,
		Packets: 20,
	})
	data, _ := readInput(path)
	// Drop the final 6 bytes so the last length-prefixed record is cut short.
	dst := filepath.Join(dir, "trunc.rtp")
	if err := writeFile(dst, data[:len(data)-6]); err != nil {
		t.Fatal(err)
	}
	var errs fault.Batch
	res, err := ReadRTPCapture(dst, "rtp-0", 48000, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if !errs.HasKind(fault.KindTruncated) {
		t.Fatal("truncated record must be reported")
	}
	if len(res.Track.Samples) == 0 {
		t.Fatal("earlier complete packets must remain committed")
	}
}
