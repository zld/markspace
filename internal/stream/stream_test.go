package stream_test

import (
	"testing"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/fixture"
	"archivecheck/internal/stream"
)

func TestAnalyzeMP4Valid(t *testing.T) {
	s := stream.AnalyzeMP4(asset.FromBytes("clip.mp4", fixture.MP4(false)))
	if s.Bad() {
		t.Fatalf("valid MP4 reported damage: %+v", s.Damaged)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.ID != fixture.MP4TrackID || tr.Handler != "vide" || tr.Timescale != fixture.MP4Timescale {
		t.Errorf("track = %+v", tr)
	}
	if tr.SampleCount != fixture.MP4SampleCount || tr.SyncSamples != 4 {
		t.Errorf("samples = %d sync = %d", tr.SampleCount, tr.SyncSamples)
	}
	if len(tr.Keyframes) != 4 {
		t.Fatalf("keyframes = %d", len(tr.Keyframes))
	}
	if tr.Keyframes[0].Offset < 0 || tr.Keyframes[0].Number != 1 {
		t.Errorf("first keyframe = %+v", tr.Keyframes[0])
	}
	if tr.Keyframes[1].Time-tr.Keyframes[0].Time != 1_000_000_000 {
		t.Errorf("keyframe interval = %d, want 1s", tr.Keyframes[1].Time-tr.Keyframes[0].Time)
	}
	if tr.Interval != 1_000_000_000 {
		t.Errorf("mean interval = %d", tr.Interval)
	}
	if tr.DurationNanos != 4_000_000_000 {
		t.Errorf("duration = %d", tr.DurationNanos)
	}
}

func TestAnalyzeMP4TruncatedReportsFirstOffset(t *testing.T) {
	valid := fixture.MP4(false)
	cut := fixture.MP4(true)
	s := stream.AnalyzeMP4(asset.FromBytes("cut.mp4", cut))
	if !s.Bad() {
		t.Fatal("truncated MP4 reported no damage")
	}
	span := s.Damaged[0]
	if span.Kind != fault.KindTruncated {
		t.Errorf("kind = %s", span.Kind)
	}
	if span.Start <= 0 || span.End != int64(len(cut)) {
		t.Errorf("span = %+v, source is %d bytes", span, len(cut))
	}
	if span.Start >= int64(len(valid)) {
		t.Errorf("offset %d is past the intact file", span.Start)
	}
}

func TestAnalyzeMP4BadBoxSize(t *testing.T) {
	data := fixture.MP4(false)
	// Patch the mdat box size to something larger than the file.
	off := 0
	for off+8 <= len(data) {
		size := int64(data[off])<<24 | int64(data[off+1])<<16 | int64(data[off+2])<<8 | int64(data[off+3])
		if string(data[off+4:off+8]) == "mdat" {
			data[off] = 0x7f
			break
		}
		off += int(size)
	}
	s := stream.AnalyzeMP4(asset.FromBytes("bad.mp4", data))
	if !s.Bad() {
		t.Fatal("oversized box not reported")
	}
	if s.Damaged[0].Start != int64(off) {
		t.Errorf("first damaged offset = %d, want %d", s.Damaged[0].Start, off)
	}
}

func TestAnalyzeMP4MoovBeforeMdatSurvivesTruncation(t *testing.T) {
	full := fixture.MP4(false)
	cut := fixture.MP4(true)
	s := stream.AnalyzeMP4(asset.FromBytes("cut.mp4", cut))
	if len(s.Tracks) != 1 {
		t.Fatalf("tracks = %d after truncation", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.Timescale != fixture.MP4Timescale || len(tr.Keyframes) != 4 {
		t.Errorf("track metadata lost: %+v", tr)
	}
	if tr.Keyframes[3].Offset >= int64(len(cut)) {
		t.Errorf("keyframe offset %d lies past the truncated file (%d bytes)", tr.Keyframes[3].Offset, len(cut))
	}
	_ = full
}

func TestAnalyzeMP4MissingMoov(t *testing.T) {
	// Drop the moov box entirely: ftyp followed by mdat only.
	data := fixture.MP4(false)
	moovAt := int64(-1)
	for off := 0; off+8 <= len(data); {
		size := int(data[off])<<24 | int(data[off+1])<<16 | int(data[off+2])<<8 | int(data[off+3])
		if string(data[off+4:off+8]) == "moov" {
			moovAt = int64(off)
			break
		}
		off += size
	}
	if moovAt < 0 {
		t.Fatal("fixture has no moov")
	}
	s := stream.AnalyzeMP4(asset.FromBytes("nomoov.mp4", data[:moovAt]))
	if len(s.Tracks) != 0 {
		t.Errorf("tracks = %d", len(s.Tracks))
	}
	if s.ParseError == "" {
		t.Error("missing moov should be a parse error")
	}
}

// rtpCapture builds the RTP fixture, failing the test on a marshalling error.
func rtpCapture(t *testing.T) []byte {
	t.Helper()
	data, err := fixture.RTP()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRTPGaps(t *testing.T) {
	capture := rtpCapture(t)
	s := stream.AnalyzeRTP(asset.FromBytes("loss.rtp", capture))
	if s.Packets != 40 {
		t.Fatalf("packets = %d", s.Packets)
	}
	if s.Lost != 5 || len(s.Gaps) != 2 {
		t.Fatalf("lost = %d gaps = %d: %+v", s.Lost, len(s.Gaps), s.Gaps)
	}
	first := s.Gaps[0]
	if first.Missing != 3 || first.Expected != 1012 || first.Got != 1015 {
		t.Errorf("first gap = %+v", first)
	}
	if first.Offset <= 0 || first.Offset >= int64(len(capture)) {
		t.Errorf("gap offset = %d", first.Offset)
	}
	if len(s.Damaged) != 2 {
		t.Errorf("damaged spans = %+v", s.Damaged)
	}
	if s.Duration <= 0 {
		t.Errorf("duration = %d", s.Duration)
	}
}

func TestRTPTruncatedRecord(t *testing.T) {
	data := rtpCapture(t)
	cut := data[:len(data)-5]
	s := stream.AnalyzeRTP(asset.FromBytes("cut.rtp", cut))
	if !s.Bad() {
		t.Fatal("truncated RTP record not reported")
	}
	span := s.Damaged[len(s.Damaged)-1]
	if span.Kind != fault.KindTruncated || span.End != int64(len(cut)) {
		t.Errorf("span = %+v", span)
	}
}

func TestRTPBuildRoundTrip(t *testing.T) {
	s := stream.AnalyzeRTP(asset.FromBytes("x.rtp", rtpCapture(t)))
	rebuilt, err := stream.BuildRTP(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt) != 0 {
		t.Errorf("empty build produced %d bytes", len(rebuilt))
	}
	if s.FirstSeq != 1000 || s.LastSeq != 1044 {
		t.Errorf("sequence range = %d..%d", s.FirstSeq, s.LastSeq)
	}
}
