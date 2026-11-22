package demux

import (
	"encoding/binary"
	"testing"

	"mediadist/internal/codec"
	"mediadist/internal/transport"
)

// mkPackets builds RTP datagrams for one access unit: n chunks of data with a
// marker on the last chunk, at the given sequence numbers and timestamp.
func mkPackets(stream int, seqs []uint16, ts uint32, chunks [][]byte) []*transport.Packet {
	var out []*transport.Packet
	for i, seq := range seqs {
		raw := make([]byte, 12+len(chunks[i]))
		raw[0] = 0x80
		raw[1] = 96
		binary.BigEndian.PutUint16(raw[2:4], seq)
		binary.BigEndian.PutUint32(raw[4:8], ts)
		if i == len(seqs)-1 {
			raw[1] |= 0x80 // marker
		}
		copy(raw[12:], chunks[i])
		p, err := transport.Parse(raw, transport.ParseOptions{Stream: stream})
		if err != nil {
			panic(err)
		}
		out = append(out, p)
	}
	return out
}

func split(data []byte, n int) [][]byte {
	var out [][]byte
	chunk := (len(data) + n - 1) / n
	for off := 0; off < len(data); off += chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		out = append(out, data[off:end])
	}
	return out
}

func sampleData(key bool, salt byte) []byte {
	var out []byte
	for _, nal := range [][]byte{{0x67, 0x42, 0x00, salt}, {0x68, 0xCE, 0x06, salt}} {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nal...)
	}
	slice := []byte{0x41, salt, salt}
	if key {
		slice[0] = 0x65
	}
	out = append(out, 0, 0, 0, 1)
	out = append(out, slice...)
	return out
}

func TestDemuxGroupsByTimestampAndAssignsDurations(t *testing.T) {
	a := sampleData(false, 1)
	b := sampleData(true, 2)
	packets := mkPackets(0, []uint16{10, 11}, 9000, split(a, 2))
	packets = append(packets, mkPackets(0, []uint16{12, 13}, 12000, split(b, 2))...)

	spec := StreamSpec{
		Stream: 0, TrackID: 1, Codec: CodecH264, Timescale: 90000,
		WrapBits: 32, FrameDuration: 3000, SegmentNS: 2e9,
	}
	dm, err := Demux([]StreamSpec{spec}, map[int][]*transport.Packet{0: packets},
		map[int][]transport.SeqSpan{}, codec.DefaultScanLimits())
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}
	st := dm.Streams[0]
	if len(st.Samples) != 2 {
		t.Fatalf("samples %d want 2", len(st.Samples))
	}
	if st.Samples[0].Dur != 3000 || st.Samples[1].Dur != 3000 {
		t.Fatalf("durations %d %d", st.Samples[0].Dur, st.Samples[1].Dur)
	}
	if st.Samples[0].Keyframe || !st.Samples[1].Keyframe {
		t.Fatalf("keyframe flags %t %t", st.Samples[0].Keyframe, st.Samples[1].Keyframe)
	}
	if st.Samples[0].Damaged || st.Samples[1].Damaged {
		t.Fatalf("clean samples marked damaged")
	}
	if st.Samples[1].PTS != 12000 {
		t.Fatalf("pts %d", st.Samples[1].PTS)
	}
	if st.Samples[1].PTSNS != 12000*1_000_000_000/90000 {
		t.Fatalf("pts ns %d", st.Samples[1].PTSNS)
	}
}

func TestDemuxMarksMissingPacketsFromCommittedLoss(t *testing.T) {
	data := sampleData(true, 1)
	chunks := split(data, 4)
	// Sequence 21 is missing from the middle of the same timestamp group.
	packets := mkPackets(0, []uint16{20, 22, 23}, 3000, [][]byte{chunks[0], chunks[2], chunks[3]})
	// The reorder buffer reports the committed hole.
	gaps := map[int][]transport.SeqSpan{0: {{Stream: 0, First: 21, Last: 21}}}

	spec := StreamSpec{Stream: 0, TrackID: 1, Codec: CodecH264,
		Timescale: 90000, WrapBits: 32, FrameDuration: 3000, SegmentNS: 2e9}
	dm, err := Demux([]StreamSpec{spec}, map[int][]*transport.Packet{0: packets},
		gaps, codec.DefaultScanLimits())
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}
	s := dm.Streams[0].Samples[0]
	if s.MissingPackets != 1 {
		t.Fatalf("missing %d want 1", s.MissingPackets)
	}
	if !s.Damaged {
		t.Fatal("sample with a hole must be damaged")
	}
	// The keyframe NAL survived, so the sample is still a keyframe.
	if !s.Keyframe {
		t.Fatal("keyframe should still be recognized")
	}
	found := false
	for _, e := range dm.Errors {
		if e.Code == "sample_missing_packets" && e.Span.Stream == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing packet error not reported: %+v", dm.Errors)
	}
}

func TestDemuxDoesNotChargeGapToFollowingSample(t *testing.T) {
	a := sampleData(false, 1)
	b := sampleData(false, 2)
	// The gap at sequence 31 belongs to the first sample's tail.
	packets := mkPackets(0, []uint16{30, 32}, 3000, split(a, 3)[0:2])
	packets = append(packets, mkPackets(0, []uint16{33, 34}, 6000, split(b, 2))...)
	gaps := map[int][]transport.SeqSpan{0: {{Stream: 0, First: 31, Last: 31}}}

	spec := StreamSpec{Stream: 0, TrackID: 1, Codec: CodecH264,
		Timescale: 90000, WrapBits: 32, FrameDuration: 3000, SegmentNS: 2e9}
	dm, err := Demux([]StreamSpec{spec}, map[int][]*transport.Packet{0: packets},
		gaps, codec.DefaultScanLimits())
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}
	if got := dm.Streams[0].Samples[1].MissingPackets; got != 0 {
		t.Fatalf("second sample charged %d missing packets", got)
	}
}

func TestDemuxSegmentAssignmentUsesOrigin(t *testing.T) {
	// 90 kHz clock, 1 s segments, origin at 90000 ticks (1 s). Samples at 1 s
	// and 2.5 s land in segments 0 and 1.
	a := sampleData(false, 1)
	b := sampleData(false, 2)
	packets := mkPackets(0, []uint16{1}, 90000, split(a, 1))
	packets = append(packets, mkPackets(0, []uint16{2}, 225000, split(b, 1))...)
	spec := StreamSpec{Stream: 0, TrackID: 1, Codec: CodecH264,
		Timescale: 90000, WrapBits: 32, FrameDuration: 3000,
		SegmentNS: 1e9, OriginNS: int64(1e9)}
	dm, err := Demux([]StreamSpec{spec}, map[int][]*transport.Packet{0: packets},
		map[int][]transport.SeqSpan{}, codec.DefaultScanLimits())
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}
	if dm.Streams[0].Samples[0].Segment != 0 {
		t.Fatalf("first sample segment %d", dm.Streams[0].Samples[0].Segment)
	}
	if dm.Streams[0].Samples[1].Segment != 1 {
		t.Fatalf("second sample segment %d", dm.Streams[0].Samples[1].Segment)
	}
}

func TestDemuxTimestampWrapAcrossSamples(t *testing.T) {
	// Two samples either side of the 32-bit wrap must expand monotonically.
	a := sampleData(false, 1)
	b := sampleData(false, 2)
	packets := mkPackets(0, []uint16{1}, 0xFFFFF000, split(a, 1))
	packets = append(packets, mkPackets(0, []uint16{2}, 0x00001000, split(b, 1))...)
	spec := StreamSpec{Stream: 0, TrackID: 1, Codec: CodecH264,
		Timescale: 90000, WrapBits: 32, FrameDuration: 3000, SegmentNS: 2e9}
	dm, err := Demux([]StreamSpec{spec}, map[int][]*transport.Packet{0: packets},
		map[int][]transport.SeqSpan{}, codec.DefaultScanLimits())
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}
	s := dm.Streams[0].Samples
	if !(s[1].PTS > s[0].PTS) {
		t.Fatalf("wrap not expanded: %d then %d", s[0].PTS, s[1].PTS)
	}
	if s[1].PTS-s[0].PTS != 0x2000 {
		t.Fatalf("wrap delta %d want %d", s[1].PTS-s[0].PTS, 0x2000)
	}
}

func TestDemuxAV1Keyframes(t *testing.T) {
	key := []byte{0x12, 0x00, 0x32, 0x04, 0x00, 0x01, 0x02, 0x03}
	inter := []byte{0x12, 0x00, 0x32, 0x04, 0x20, 0x01, 0x02, 0x03}
	packets := mkPackets(2, []uint16{1}, 1000, split(key, 1))
	packets = append(packets, mkPackets(2, []uint16{2}, 4000, split(inter, 1))...)
	spec := StreamSpec{Stream: 2, TrackID: 3, Codec: CodecAV1,
		Timescale: 90000, WrapBits: 32, FrameDuration: 3000, SegmentNS: 2e9}
	dm, err := Demux([]StreamSpec{spec}, map[int][]*transport.Packet{2: packets},
		map[int][]transport.SeqSpan{}, codec.DefaultScanLimits())
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}
	if !dm.Streams[0].Samples[0].Keyframe {
		t.Fatal("AV1 key frame not recognized")
	}
	if dm.Streams[0].Samples[1].Keyframe {
		t.Fatal("AV1 inter frame marked as keyframe")
	}
}

func TestDemuxTrackSamplesOrdered(t *testing.T) {
	a := sampleData(false, 1)
	packets := mkPackets(0, []uint16{1}, 3000, split(a, 1))
	packets = append(packets, mkPackets(0, []uint16{2}, 9000, split(a, 1))...)
	spec := StreamSpec{Stream: 0, TrackID: 1, Codec: CodecH264,
		Timescale: 90000, WrapBits: 32, FrameDuration: 3000, SegmentNS: 2e9}
	dm, err := Demux([]StreamSpec{spec}, map[int][]*transport.Packet{0: packets},
		map[int][]transport.SeqSpan{}, codec.DefaultScanLimits())
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}
	byTrack := dm.TrackSamples()
	samples := byTrack[1]
	if len(samples) != 2 || samples[0].PTS >= samples[1].PTS {
		t.Fatalf("track samples not ordered: %+v", samples)
	}
	if _, ok := dm.SampleAt(1, 0); !ok {
		t.Fatal("SampleAt failed for track 1 index 0")
	}
}
