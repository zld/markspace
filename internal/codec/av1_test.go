package codec

import (
	"testing"

	"archivecheck/internal/media"
)

func av1Fixture() []byte {
	var out []byte
	out = append(out, EncodeOBU(OBUSequenceHeader, []byte{1, 2, 3, 4})...)
	out = append(out, EncodeOBU(OBUFrame, []byte{9, 9, 9, 9, 9, 9})...) // keyframe
	out = append(out, EncodeOBU(OBUTileGroup, []byte{7, 7})...)         // inter frame
	out = append(out, EncodeOBU(OBUFrame, []byte{5, 5, 5, 5, 5})...)    // keyframe
	return out
}

func TestAV1ChunkInvariance(t *testing.T) {
	src := av1Fixture()
	var baseline AV1Report
	for chunk := 1; chunk <= len(src); chunk++ {
		p := NewAV1Parser()
		var obus []OBU
		for off := 0; off < len(src); off += chunk {
			end := off + chunk
			if end > len(src) {
				end = len(src)
			}
			obus = append(obus, p.Feed(src[off:end])...)
		}
		obus = append(obus, p.Flush()...)
		rep := p.Report()
		if chunk == 1 {
			baseline = rep
			if rep.Units != 4 || rep.Keyframes != 2 || rep.Frames != 3 || rep.Truncated != 0 {
				t.Fatalf("baseline=%+v", rep)
			}
			continue
		}
		if rep != baseline {
			t.Fatalf("chunk=%d report=%+v baseline=%+v", chunk, rep, baseline)
		}
		if len(obus) != 4 {
			t.Fatalf("chunk=%d obus=%d", chunk, len(obus))
		}
		for _, o := range obus {
			if o.Truncated {
				t.Fatalf("chunk=%d unexpected truncation %+v", chunk, o)
			}
		}
	}
}

func TestAV1TruncationDetected(t *testing.T) {
	src := av1Fixture()
	// Cut inside the final keyframe OBU's payload.
	cut := len(src) - 3
	p := NewAV1Parser()
	var obus []OBU
	for off := 0; off < cut; off += 2 {
		end := off + 2
		if end > cut {
			end = cut
		}
		obus = append(obus, p.Feed(src[off:end])...)
	}
	obus = append(obus, p.Flush()...)
	rep := p.Report()
	if rep.Truncated != 1 {
		t.Fatalf("report=%+v", rep)
	}
	last := obus[len(obus)-1]
	if !last.Truncated || !last.Keyframe {
		t.Fatalf("last=%+v", last)
	}
	if rep.FirstDamage != last.Offset {
		t.Fatalf("first damage=%d offset=%d", rep.FirstDamage, last.Offset)
	}
	fs := p.Faults("high")
	if len(fs) != 1 || fs[0].Kind != media.KindOBUTruncated || fs[0].Class != media.Unrecoverable {
		t.Fatalf("faults=%+v", fs)
	}
}

func TestAV1RejectsMissingSizeField(t *testing.T) {
	p := NewAV1Parser()
	p.Feed([]byte{OBUFrame << 3, 1, 2, 3}) // has_size_field clear
	p.Flush()
	rep := p.Report()
	if rep.HeaderDamage == 0 && rep.Truncated == 0 {
		t.Fatalf("unsized OBU was not reported: %+v", rep)
	}
}

func TestAV1LargeOBUAcrossChunks(t *testing.T) {
	payload := make([]byte, 300)
	for i := range payload {
		payload[i] = byte(i%255 + 1)
	}
	src := EncodeOBU(OBUFrame, payload)
	p := NewAV1Parser()
	var got []OBU
	for off := 0; off < len(src); off += 7 {
		end := off + 7
		if end > len(src) {
			end = len(src)
		}
		got = append(got, p.Feed(src[off:end])...)
	}
	got = append(got, p.Flush()...)
	if len(got) != 1 || got[0].Payload != 300 || got[0].Truncated {
		t.Fatalf("got=%+v", got)
	}
	if got[0].End() != int64(len(src)) {
		t.Fatalf("end=%d len=%d", got[0].End(), len(src))
	}
}
