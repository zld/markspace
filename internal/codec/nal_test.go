package codec

import (
	"bytes"
	"testing"

	"archivecheck/internal/media"
)

// feedAll feeds src in fixed-size chunks and returns every emitted unit.
func feedAll(t *testing.T, c Codec, src []byte, chunk int) ([]NALUnit, AnnexBReport) {
	t.Helper()
	p, err := NewNALParser(c)
	if err != nil {
		t.Fatal(err)
	}
	var units []NALUnit
	for off := 0; off < len(src); off += chunk {
		end := off + chunk
		if end > len(src) {
			end = len(src)
		}
		units = append(units, p.Feed(src[off:end])...)
	}
	units = append(units, p.Flush()...)
	return units, p.Report()
}

func h264Fixture() []byte {
	return []byte{
		0, 0, 0, 1, 0x67, 0xAA, // SPS (not a frame)
		0, 0, 0, 1, 0x65, 0x11, 0x22, // IDR frame
		0, 0, 1, 0x41, 0x33, // non-IDR frame, 3-byte prefix
		0, 0, 0, 1, 0x65, 0x44, 0x55, 0x66, // final IDR frame
	}
}

func TestNALParserChunkInvariance(t *testing.T) {
	src := h264Fixture()
	var want []NALUnit
	for chunk := 1; chunk <= len(src); chunk++ {
		units, rep := feedAll(t, H264, src, chunk)
		if len(units) != 4 {
			t.Fatalf("chunk=%d units=%d want 4: %+v", chunk, len(units), units)
		}
		if rep.Units != 4 || rep.Keyframes != 2 {
			t.Fatalf("chunk=%d report=%+v", chunk, rep)
		}
		if rep.Truncated != 0 || rep.FirstDamage != -1 {
			t.Fatalf("chunk=%d unexpected truncation: %+v", chunk, rep)
		}
		byOffset := map[int64]NALUnit{}
		for _, u := range units {
			byOffset[u.Offset] = u
		}
		if chunk == 1 {
			want = append([]NALUnit(nil), units...)
			continue
		}
		// Offsets, types, lengths and keyframe flags must be chunk independent.
		for _, w := range want {
			got, ok := byOffset[w.Offset]
			if !ok {
				t.Fatalf("chunk=%d missing unit at %d", chunk, w.Offset)
			}
			if got.HeaderAt != w.HeaderAt || got.Length != w.Length || got.Type != w.Type || got.Keyframe != w.Keyframe {
				t.Fatalf("chunk=%d unit %d = %+v want %+v", chunk, w.Offset, got, w)
			}
		}
	}
}

func TestNALParserSplitStartCodes(t *testing.T) {
	src := h264Fixture()
	units, rep := feedAll(t, H264, src, 2)
	if rep.SplitStartCodes == 0 {
		t.Fatalf("expected split start codes with 2-byte chunks: %+v", rep)
	}
	splitSeen := 0
	for _, u := range units {
		if u.SplitOnRead {
			splitSeen++
		}
	}
	if splitSeen != rep.SplitStartCodes {
		t.Fatalf("split flags=%d report=%d", splitSeen, rep.SplitStartCodes)
	}
	// The 4-byte prefix at offset 18 must be found even when split 1/3.
	units, _ = feedAll(t, H264, src, 19)
	found := false
	for _, u := range units {
		if u.Offset == 18 && u.Keyframe && u.SplitOnRead {
			found = true
		}
	}
	if !found {
		t.Fatalf("boundary-split 4-byte prefix was not reassembled: %+v", units)
	}
}

func TestNALParserTruncation(t *testing.T) {
	// The stream ends on a bare prefix: no header byte ever arrived, so the
	// unit is a provable truncation at the prefix offset.
	src := []byte{0, 0, 0, 1, 0x65, 0xAA, 0xBB, 0xC0, 0, 0, 1}
	units, rep := feedAll(t, H264, src, 3)
	if rep.Truncated != 1 {
		t.Fatalf("report=%+v", rep)
	}
	last := units[len(units)-1]
	// The trailing bytes 00 00 01 form a valid 3-byte prefix at offset 8.
	if !last.Truncated || last.Length != 0 || last.Offset != 8 {
		t.Fatalf("last=%+v", last)
	}
	if rep.FirstDamage != 8 {
		t.Fatalf("first damage=%d", rep.FirstDamage)
	}

	// A final unit that did receive its header has no explicit length in
	// Annex B; it is reported complete rather than guessed truncated.
	units, rep = feedAll(t, H264, []byte{0, 0, 0, 1, 0x65, 1, 2}, 4)
	if rep.Truncated != 0 || len(units) != 1 || units[0].Length != 3 {
		t.Fatalf("report=%+v units=%+v", rep, units)
	}
}

func TestH265TypesAndKeyframes(t *testing.T) {
	src := []byte{
		0, 0, 0, 1, 32 << 1, 1, 0xAA, // VPS: not a frame
		0, 0, 0, 1, 19 << 1, 1, 0xBB, // IDR_W_RADL: keyframe
		0, 0, 0, 1, 1 << 1, 1, 0xCC, // TRAIL_R: inter frame
	}
	units, rep := feedAll(t, H265, src, 1)
	if rep.Keyframes != 1 || rep.Truncated != 0 {
		t.Fatalf("report=%+v", rep)
	}
	if units[0].Type != 32 || units[0].Frame {
		t.Fatalf("VPS misclassified: %+v", units[0])
	}
	if units[1].Type != 19 || !units[1].Keyframe || !units[1].Frame {
		t.Fatalf("IDR misclassified: %+v", units[1])
	}
	if units[2].Type != 1 || units[2].Keyframe || !units[2].Frame {
		t.Fatalf("TRAIL misclassified: %+v", units[2])
	}
}

func TestStartCodeLen(t *testing.T) {
	cases := []struct {
		b    []byte
		want int
	}{
		{[]byte{0, 0, 1}, 3},
		{[]byte{0, 0, 0, 1}, 4},
		{[]byte{0, 0, 0, 0, 1}, 0}, // scan advances past the first zero
		{[]byte{0, 0, 0, 1, 0}, 4}, // resolved once four bytes exist
		{[]byte{0, 0, 2}, 0},
		{[]byte{0, 0}, 0},
	}
	for _, c := range cases {
		if got := StartCodeLen(c.b, 0); got != c.want {
			t.Fatalf("StartCodeLen(%v)=%d want %d", c.b, got, c.want)
		}
	}
}

func TestNALFaults(t *testing.T) {
	p, _ := NewNALParser(H264)
	p.Feed([]byte{0, 0, 0, 1, 0x65, 1, 2, 0, 0, 1})
	p.Flush()
	fs := p.Faults("low")
	if len(fs) != 1 || fs[0].Kind != media.KindNALTruncated || fs[0].ByteOffset != 7 {
		t.Fatalf("faults=%+v", fs)
	}
	if fs[0].Class != media.Unrecoverable {
		t.Fatalf("class=%v", fs[0].Class)
	}
	if _, err := NewNALParser(AV1); err == nil {
		t.Fatal("AV1 accepted as Annex B")
	}
}

func TestInvalidCodecRejected(t *testing.T) {
	if _, err := NewNALParser("vp9"); err == nil {
		t.Fatal("unknown codec accepted")
	}
	if !bytes.Equal(EncodeOBU(OBUTemporalDelim, []byte{1, 2}), []byte{0x12, 0x02, 1, 2}) {
		t.Fatalf("EncodeOBU header mismatch")
	}
}
