package codec

import (
	"encoding/binary"
	"testing"
)

// obu builds a length-prefixed AV1 OBU: 4-byte length + OBU header + payload.
func obu(obuType int, payload []byte, keyframe bool) []byte {
	hdr := byte(obuType<<3) | obuHasSize
	body := []byte{hdr}
	if obuType == OBUFrame || obuType == OBUFrameHeader {
		// Frame header syntax: show_existing_frame(1)=0, frame_type(2),
		// then padding. KEY_FRAME=0, INTER_FRAME=1.
		ft := byte(1) // INTER
		if keyframe {
			ft = 0 // KEY
		}
		body = append(body, ft<<5)
	}
	body = append(body, payload...)
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	return append(out, body...)
}

func TestOBUHeaderParse(t *testing.T) {
	h := parseOBUHeader(byte(OBUSequenceHeader<<3) | obuHasSize)
	if h.Type != OBUSequenceHeader || !h.HasSize || h.HeaderSize != 1 {
		t.Fatalf("seq header parse wrong: %+v", h)
	}
	ext := parseOBUHeader(byte(OBUFrame<<3) | obuHasSize | obuExtension)
	if !ext.HasExt || ext.HeaderSize != 2 {
		t.Fatalf("extension header wrong: %+v", ext)
	}
}

func TestAV1KeyframeClassification(t *testing.T) {
	key := obu(OBUFrame, []byte{0xaa, 0xbb}, true)
	inter := obu(OBUFrame, []byte{0xcc}, false)
	seq := obu(OBUSequenceHeader, []byte{0x01}, false)

	sc := newLengthPrefixScanner(AV1)
	stream := append(append(seq, key...), inter...)
	var units []Unit
	if err := sc.Push(stream, 0, &units); err != nil {
		t.Fatal(err)
	}
	if g := sc.Flush(int64(len(stream)), &units); len(g) != 0 {
		t.Fatalf("unexpected gaps: %+v", g)
	}
	if len(units) != 3 {
		t.Fatalf("units=%d want 3", len(units))
	}
	if units[0].TypeName != "sequence-header" || units[0].Keyframe {
		t.Fatalf("seq OBU wrong: %+v", units[0])
	}
	if !units[1].Keyframe || units[1].Type != OBUFrame {
		t.Fatalf("keyframe OBU wrong: %+v", units[1])
	}
	if units[2].Keyframe {
		t.Fatalf("inter frame must not be keyframe: %+v", units[2])
	}
}

func TestFramedScannerTruncatedTail(t *testing.T) {
	good := obu(OBUFrame, []byte{1, 2, 3}, true)
	// Declare a 10-byte payload but supply only 3 bytes, then EOF.
	bad := make([]byte, 4)
	binary.BigEndian.PutUint32(bad, 10)
	bad = append(bad, 0x12, 0x34, 0x56)
	stream := append(good, bad...)

	sc := newLengthPrefixScanner(AV1)
	var units []Unit
	if err := sc.Push(stream, 0, &units); err != nil {
		t.Fatal(err)
	}
	gaps := sc.Flush(int64(len(stream)), &units)
	if len(units) != 1 || !units[0].Keyframe {
		t.Fatalf("good unit must survive: %+v", units)
	}
	if len(gaps) != 1 || gaps[0].Code != "parse.truncated-tail" {
		t.Fatalf("want one truncated gap, got %+v", gaps)
	}
	// good keyframe OBU: prefix(4)+header(1)+frame byte(1)+payload(3) = 9.
	// The bad prefix starts at 9 and is itself part of the damage span.
	if gaps[0].Start != 9 {
		t.Fatalf("gap start=%d want 9", gaps[0].Start)
	}
}

func TestFramedScannerInvalidLengthResyncs(t *testing.T) {
	good1 := obu(OBUSequenceHeader, []byte{0x09}, false)
	good2 := obu(OBUFrame, []byte{0xde, 0xad}, true)

	// A bogus prefix claiming 0x10000 bytes, followed by garbage, then the
	// real good2 unit. Resync must find good2 and keep good1 intact.
	bogus := make([]byte, 4)
	binary.BigEndian.PutUint32(bogus, 0x10000)
	garbage := []byte{0xff, 0xee, 0xdd}
	stream := append(good1, bogus...)
	stream = append(stream, garbage...)
	stream = append(stream, good2...)

	sc := newLengthPrefixScanner(AV1)
	var units []Unit
	if err := sc.Push(stream, 0, &units); err != nil {
		t.Fatal(err)
	}
	gaps := sc.Flush(int64(len(stream)), &units)
	if len(units) != 2 {
		t.Fatalf("units=%d want 2 (good1 + resynced good2)", len(units))
	}
	if !units[1].Keyframe {
		t.Fatalf("resynced unit must be the keyframe: %+v", units[1])
	}
	if len(gaps) != 1 || gaps[0].Code != "parse.invalid-length" {
		t.Fatalf("want one invalid-length gap, got %+v", gaps)
	}
	// The corrupt span starts at good1's end (the bogus prefix) and is
	// clamped to the embedded good2 prefix: bogus(4)+garbage(3) = 7 bytes.
	wantGapStart := int64(len(good1))
	wantGapEnd := wantGapStart + int64(len(bogus)+len(garbage))
	if gaps[0].Start != wantGapStart || gaps[0].End != wantGapEnd {
		t.Fatalf("gap bounds wrong: %+v want [%d,%d)",
			gaps[0], wantGapStart, wantGapEnd)
	}
	if gaps[0].End != units[1].Start-4 {
		t.Fatal("gap must not extend over the resynced valid unit")
	}
}

func TestFramedScannerCrossChunkPrefixAndPayload(t *testing.T) {
	stream := append(obu(OBUFrame, []byte{1, 2}, true),
		obu(OBUFrame, []byte{3}, false)...)
	for cut := 1; cut < len(stream); cut++ {
		sc := newLengthPrefixScanner(H264)
		var units []Unit
		if err := sc.Push(stream[:cut], 0, &units); err != nil {
			t.Fatal(err)
		}
		if err := sc.Push(stream[cut:], int64(cut), &units); err != nil {
			t.Fatal(err)
		}
		if g := sc.Flush(int64(len(stream)), &units); len(g) != 0 {
			t.Fatalf("cut=%d gaps: %+v", cut, g)
		}
		if len(units) != 2 {
			t.Fatalf("cut=%d units=%d want 2", cut, len(units))
		}
	}
}

func TestNewScannerValidation(t *testing.T) {
	if _, err := NewScanner(AV1, AnnexB); err == nil {
		t.Fatal("AV1 Annex B must be rejected")
	}
	if _, err := NewScanner(Codec("vp9"), LengthPrefixed); err == nil {
		t.Fatal("unknown codec must be rejected")
	}
	if _, err := NewScanner(H264, Framing("bogus")); err == nil {
		t.Fatal("unknown framing must be rejected")
	}
	if _, err := NewScanner(H265, AnnexB); err != nil {
		t.Fatalf("H265 Annex B should be allowed: %v", err)
	}
}
