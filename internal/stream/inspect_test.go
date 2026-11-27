package stream_test

import (
	"testing"

	"media-check/internal/fault"
	"media-check/internal/fixture"
	"media-check/internal/rtcp"
	rtpbuf "media-check/internal/rtp"
	"media-check/internal/stream"
)

// receive runs the fixture stream through the per-SSRC reorder buffers and
// returns the globally ordered packets.
func receive(t *testing.T, s *fixture.Stream, fm *fault.Model) []rtpbuf.OrderedPacket {
	t.Helper()
	buffers := map[uint32]*rtpbuf.ReorderBuffer{}
	var out []rtpbuf.OrderedPacket
	for _, rp := range s.Packets {
		b, ok := buffers[rp.Packet.SSRC]
		if !ok {
			b = rtpbuf.NewReorderBuffer(rp.TrackID, rp.Packet.SSRC, rtpbuf.DefaultWindowSize, fm)
			buffers[rp.Packet.SSRC] = b
		}
		out = append(out, b.Push(rp.Packet)...)
	}
	for _, b := range buffers {
		out = append(out, b.Flush()...)
	}
	return out
}

func buildMapper(t *testing.T, s *fixture.Stream) *rtcp.TimeMapper {
	t.Helper()
	fm := fault.NewModel()
	m := rtcp.NewTimeMapper(fm)
	for _, r := range s.Reports {
		if err := m.AddReport(r); err != nil {
			t.Fatalf("add report: %v", err)
		}
	}
	return m
}

func metaFor(s *fixture.Stream) map[uint32]stream.TrackMeta {
	meta := make(map[uint32]stream.TrackMeta)
	for _, c := range s.Tracks {
		meta[c.SSRC] = stream.TrackMeta{
			TrackID: c.ID, Kind: c.Kind, ClockRate: c.ClockRate, KeyEvery: c.KeyEvery,
		}
	}
	return meta
}

func TestHealthyStreamStructure(t *testing.T) {
	fm := fault.NewModel()
	s, err := fixture.Build(17, fixture.DefaultVideoConfig(), fixture.DefaultAudioConfig(), fixture.DamageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	mapper := buildMapper(t, s)
	insp := stream.NewInspector(mapper, fm, metaFor(s))
	rep, err := insp.Inspect(receive(t, s, fm))
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(rep.Tracks))
	}
	v := rep.VideoTrack()
	if v == nil {
		t.Fatal("video track missing")
	}
	// 120 video units, key every 30 -> 4 key frames at indices 0,30,60,90.
	if len(v.KeyIndices) != 4 {
		t.Fatalf("key frames = %v, want 4", v.KeyIndices)
	}
	wantKeys := []uint32{0, 30, 60, 90}
	for i, k := range wantKeys {
		if v.KeyIndices[i] != k {
			t.Fatalf("key[%d] = %d, want %d", i, v.KeyIndices[i], k)
		}
	}
	if len(v.Intervals) != 3 {
		t.Fatalf("key intervals = %d, want 3", len(v.Intervals))
	}
	for _, iv := range v.Intervals {
		if iv.Samples != 30 {
			t.Fatalf("interval sample count = %d, want 30", iv.Samples)
		}
		if iv.Anomalous {
			t.Fatalf("healthy interval %+v must not be anomalous", iv)
		}
		// 3000 ticks @90kHz = 1000 ms exactly.
		if iv.Duration.Millis() != 1000 {
			t.Fatalf("interval duration = %s, want 1000 ms", iv.Duration)
		}
		if iv.SeqEnd <= iv.SeqStart {
			t.Fatalf("interval seq range invalid: %d..%d", iv.SeqStart, iv.SeqEnd)
		}
	}
	// Sample time range must span ~4 s on the rational base.
	span := v.SampleTimeRange[1].Sub(v.SampleTimeRange[0])
	if span.Millis() < 3900 || span.Millis() > 4100 {
		t.Fatalf("video time span = %s, want ~4000 ms", span)
	}
}

func TestSyncErrorWithin20ms(t *testing.T) {
	fm := fault.NewModel()
	s, err := fixture.Build(17, fixture.DefaultVideoConfig(), fixture.DefaultAudioConfig(), fixture.DamageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	mapper := buildMapper(t, s)
	rep, err := stream.NewInspector(mapper, fm, metaFor(s)).Inspect(receive(t, s, fm))
	if err != nil {
		t.Fatal(err)
	}
	sync, ok := stream.ComputeSyncError(rep)
	if !ok {
		t.Fatal("sync computation needs both tracks")
	}
	if sync.P95Millis > 20 {
		t.Fatalf("P95 sync error = %d ms, want <= 20", sync.P95Millis)
	}
	if sync.MaxAbsMillis > 20 {
		t.Fatalf("max sync error = %d ms, want <= 20", sync.MaxAbsMillis)
	}
}

func TestCorruptAndTruncatedPacketsRecordedButScanContinues(t *testing.T) {
	fm := fault.NewModel()
	video := fixture.DefaultVideoConfig()
	audio := fixture.DefaultAudioConfig()
	s, err := fixture.Build(17, video, audio, fixture.DamageConfig{
		CorruptUnits:  map[uint32][]int{video.ID: {10}},
		TruncateUnits: map[uint32][]int{video.ID: {11}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapper := buildMapper(t, s)
	rep, err := stream.NewInspector(mapper, fm, metaFor(s)).Inspect(receive(t, s, fm))
	if err != nil {
		t.Fatal(err)
	}
	v := rep.VideoTrack()
	// Two video units failed to parse; the other 118 must still be present.
	if len(v.Samples) != video.Units-2 {
		t.Fatalf("parsed samples = %d, want %d", len(v.Samples), video.Units-2)
	}
	codes := fm.CountByCode()
	if codes[fault.CodeCorruptPayload] != 1 || codes[fault.CodeTruncated] != 1 {
		t.Fatalf("fault counts = %v, want 1 corrupt + 1 truncated", codes)
	}
	var corrupt *fault.Fault
	all := fm.All()
	for i := range all {
		if all[i].Code == fault.CodeCorruptPayload {
			corrupt = &all[i]
		}
	}
	if corrupt == nil || corrupt.FirstErrOffset != int64(fixture.MediaHeaderLen) {
		t.Fatalf("corrupt first error offset = %+v, want %d", corrupt, fixture.MediaHeaderLen)
	}
}

func TestLossProducesAnomalousKeyInterval(t *testing.T) {
	fm := fault.NewModel()
	video := fixture.DefaultVideoConfig()
	// Remove the key frame at unit 30: the 30->60 interval disappears and
	// the surviving 0->60 span must be flagged anomalous with a seq range.
	s, err := fixture.Build(17, video, fixture.DefaultAudioConfig(), fixture.DamageConfig{
		LossUnits: map[uint32][]int{video.ID: {30}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapper := buildMapper(t, s)
	rep, err := stream.NewInspector(mapper, fm, metaFor(s)).Inspect(receive(t, s, fm))
	if err != nil {
		t.Fatal(err)
	}
	v := rep.VideoTrack()
	var anomaly *stream.KeyInterval
	for i := range v.Intervals {
		if v.Intervals[i].Anomalous {
			anomaly = &v.Intervals[i]
		}
	}
	if anomaly == nil {
		t.Fatalf("expected an anomalous interval after key-frame loss: %+v", v.Intervals)
	}
	if anomaly.Samples != 60 {
		t.Fatalf("anomalous span samples = %d, want 60", anomaly.Samples)
	}
	if anomaly.SeqEnd <= anomaly.SeqStart {
		t.Fatal("anomalous interval must carry its original packet seq range")
	}
}
