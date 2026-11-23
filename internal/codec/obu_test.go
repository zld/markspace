package codec

import (
	"testing"

	"mediaroute/internal/clock"
)

func obu(typ byte, payload []byte, keyframe bool) []byte {
	header := (typ << 3) | 0x02 // obu_has_size_field=1
	out := []byte{header}
	// LEB128 size; synthetic payloads are well under 128 bytes.
	size := len(payload)
	if keyframe {
		payload = append([]byte{0x80}, payload...)
		size++
	}
	out = append(out, byte(size))
	return append(out, payload...)
}

func TestOBUKeyframeAndParse(t *testing.T) {
	// Sequence header + key frame header + tile group, all length-delimited.
	frame := obu(OBUSequenceHeader, []byte{0x10}, false)
	frame = append(frame, obu(OBUFrameHeader, []byte{0x00}, true)...)
	frame = append(frame, obu(OBUTileGroup, []byte{0xAA, 0xBB}, false)...)
	obus, err := SplitOBU(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(obus) != 3 {
		t.Fatalf("OBU count = %d", len(obus))
	}
	for _, o := range obus {
		if !o.Complete || !o.HasSize {
			t.Fatalf("OBU should be complete: %+v", o)
		}
	}
	got, err := FrameKeyAV1(frame)
	if err != nil || !got {
		t.Fatalf("AV1 keyframe not detected: %v %v", got, err)
	}
}

func TestOBUNonKeyframe(t *testing.T) {
	// Frame header with keyframe bit clear.
	frame := obu(OBUFrameHeader, []byte{0x01, 0x02}, false)
	got, err := FrameKeyAV1(frame)
	if err != nil || got {
		t.Fatalf("inter frame seen as keyframe: %v %v", got, err)
	}
}

func TestOBUTruncatedNoHit(t *testing.T) {
	good := obu(OBUFrameHeader, []byte{0x00}, true)
	// Declare a payload size larger than the bytes actually present.
	trunc := append([]byte{}, good...)
	trunc[1] = 60 // lie about LEB128 size
	obus, err := SplitOBU(trunc)
	if err != nil {
		t.Fatalf("truncation should be flagged on OBU, not as parse error: %v", err)
	}
	if len(obus) != 1 || obus[0].Complete {
		t.Fatalf("truncated OBU misclassified: %+v", obus)
	}
	got, err := FrameKeyAV1(trunc)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("truncated AV1 OBU must not produce a keyframe hit")
	}
}

func TestOBUBadHeaderAndLEB(t *testing.T) {
	// Forbidden bit set.
	if _, err := SplitOBU([]byte{0x80, 0x01, 0x00}); err == nil {
		t.Fatal("forbidden OBU header accepted")
	}
	// LEB128 run-on with no terminating byte.
	if _, err := SplitOBU([]byte{0x12, 0x80, 0x80}); err == nil {
		t.Fatal("unterminated LEB128 accepted")
	}
	// Size-less OBU is unsupported in this pipeline.
	_, err := SplitOBU([]byte{0x10, 0x01, 0x00})
	if pe, ok := clock.AsError(err); !ok || pe.Recoverable {
		t.Fatalf("unsupported layout error model wrong: %v", err)
	}
}

func TestOBUMultiByteLEB128(t *testing.T) {
	// 300-byte payload encoded as LEB128 (0xAC, 0x02).
	payload := make([]byte, 300)
	frame := append([]byte{(OBUTileGroup << 3) | 0x02, 0xAC, 0x02}, payload...)
	obus, err := SplitOBU(frame)
	if err != nil || len(obus) != 1 || !obus[0].Complete || len(obus[0].Payload) != 300 {
		t.Fatalf("multi-byte LEB128 parse failed: %+v err=%v", obus, err)
	}
}
