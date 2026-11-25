package stream

import (
	"testing"

	"github.com/pion/rtp"

	rtppkg "mediacheck/internal/rtp"
	"mediacheck/internal/fault"
)

func mkOrdered(seq uint64, raw uint16, p *rtp.Packet) rtppkg.OrderedPacket {
	return rtppkg.OrderedPacket{ExpandedSeq: seq, RawSeq: raw, Packet: p}
}

func mkPacket(ts uint32, marker bool, payload []byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SSRC: 1,
			Timestamp: ts, Marker: marker,
		},
		Payload: payload,
	}
}

func TestScanSingleNALSamples(t *testing.T) {
	s := NewScanner(CodecH264)
	s.Push(mkOrdered(0, 0, mkPacket(0, true, []byte{0x65, 1, 2, 3})))
	s.Push(mkOrdered(1, 1, mkPacket(3600, true, []byte{0x41, 4, 5})))
	res := s.Flush()

	if len(res.Samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(res.Samples))
	}
	if !res.Samples[0].KeyFrame || res.Samples[1].KeyFrame {
		t.Fatalf("keyframe flags wrong: %+v", res.Samples)
	}
	if res.Samples[0].FirstSeq != 0 || res.Samples[0].LastSeq != 0 {
		t.Fatalf("seq mapping wrong: %+v", res.Samples[0])
	}
	if res.Samples[0].Byte.Length != 4 || res.Samples[1].Byte.Offset != 4 {
		t.Fatalf("byte ranges wrong: %+v %+v", res.Samples[0].Byte, res.Samples[1].Byte)
	}
	if res.Samples[0].Duration != 3600 || res.Samples[1].Duration != 3600 {
		t.Fatalf("durations wrong: %d %d", res.Samples[0].Duration, res.Samples[1].Duration)
	}
	if len(res.Faults) != 0 {
		t.Fatalf("clean stream must have no faults: %+v", res.Faults)
	}
}

func TestScanFUAFragmentation(t *testing.T) {
	s := NewScanner(CodecH264)
	indicator := byte(0x7C) // F=0,NRI=3,type=28 (FU-A)
	s.Push(mkOrdered(0, 0, mkPacket(9000, false, []byte{indicator, 0x85, 0xAA, 0xBB})))
	s.Push(mkOrdered(1, 1, mkPacket(9000, false, []byte{indicator, 0x05, 0xCC})))
	s.Push(mkOrdered(2, 2, mkPacket(9000, true, []byte{indicator, 0x45, 0xDD, 0xEE})))
	res := s.Flush()

	if len(res.Samples) != 1 || !res.Samples[0].KeyFrame {
		t.Fatalf("want one IDR sample, got %+v", res.Samples)
	}
	nal := res.Samples[0].Contributed[0]
	want := []byte{0x65, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}
	if string(nal.Data) != string(want) {
		t.Fatalf("reassembled NAL = %v, want %v", nal.Data, want)
	}
	if nal.FirstSeq != 0 || nal.LastSeq != 2 {
		t.Fatalf("FU seq span wrong: %+v", nal)
	}
}

func TestTruncatedFragmentIsLocalized(t *testing.T) {
	s := NewScanner(CodecH264)
	indicator := byte(0x7C)
	s.Push(mkOrdered(0, 0, mkPacket(0, false, []byte{indicator, 0x85, 1})))
	s.Push(mkOrdered(1, 1, mkPacket(0, false, []byte{indicator, 0x05, 2})))
	s.Push(mkOrdered(2, 2, mkPacket(3600, true, []byte{0x41, 9})))
	res := s.Flush()

	var trunc *fault.Fault
	for i := range res.Faults {
		if res.Faults[i].Class == fault.ClassTruncatedNAL {
			trunc = &res.Faults[i]
		}
	}
	if trunc == nil {
		t.Fatalf("truncated fragment not reported: %+v", res.Faults)
	}
	if trunc.Seq.First != 0 || trunc.Seq.Last != 1 {
		t.Fatalf("truncation seq span = %+v, want 0..1", trunc.Seq)
	}
	if len(res.Samples) != 1 || res.Samples[0].KeyFrame {
		t.Fatalf("post-damage sample missing: %+v", res.Samples)
	}
}

func TestIllegalAndUnknownNALRecorded(t *testing.T) {
	s := NewScanner(CodecH264)
	s.Push(mkOrdered(0, 0, mkPacket(0, true, []byte{0x1E, 7}))) // NAL type 30
	s.Push(mkOrdered(1, 1, mkPacket(3600, true, []byte{0x7C, 0x05, 8})))
	res := s.Flush()

	classes := map[fault.Class]bool{}
	for _, f := range res.Faults {
		classes[f.Class] = true
	}
	if !classes[fault.ClassUnknownNAL] || !classes[fault.ClassTruncatedNAL] {
		t.Fatalf("want unknown+truncated faults, got %+v", res.Faults)
	}
	if len(res.Samples) != 1 || res.Samples[0].NALTypes[0] != 30 {
		t.Fatalf("unknown NAL should still form a sample: %+v", res.Samples)
	}
}

func TestH265FragmentAndKeyFrame(t *testing.T) {
	s := NewScanner(CodecH265)
	indicator := []byte{0x62, 0x01} // type 49 (FU), two-byte header
	// RFC 7798 FU byte: S=0x80, E=0x40, FuType in low 6 bits. IDR type
	// 19 (0x13): start 0x93, middle 0x13, end 0x53.
	s.Push(mkOrdered(0, 0, mkPacket(0, false, append(append([]byte{}, indicator...), 0x93, 0xAA))))
	s.Push(mkOrdered(1, 1, mkPacket(0, false, append(append([]byte{}, indicator...), 0x13, 0xBB))))
	s.Push(mkOrdered(2, 2, mkPacket(0, true, append(append([]byte{}, indicator...), 0x53, 0xCC))))
	res := s.Flush()
	if len(res.Samples) != 1 || !res.Samples[0].KeyFrame {
		t.Fatalf("want one HEVC IDR sample: %+v", res.Samples)
	}
	nal := res.Samples[0].Contributed[0]
	if (nal.Data[0]>>1)&0x3F != 19 {
		t.Fatalf("HEVC NAL type wrong: %#x", nal.Data[0])
	}
}
