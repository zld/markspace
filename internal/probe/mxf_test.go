package probe

import (
	"encoding/binary"
	"testing"

	"mediapack/internal/model"
)

// klv builds one KLV item with a 16-byte SMPTE-style key whose last two bytes
// carry the local tag, followed by a short-form BER length.
func klv(tag uint16, payload []byte) []byte {
	key := make([]byte, 16)
	copy(key[0:4], klvPrefix[:])
	binary.BigEndian.PutUint16(key[14:16], tag)
	if len(payload) >= 0x80 {
		panic("test builder: use long form for large payloads")
	}
	return append(append(key, byte(len(payload))), payload...)
}

func mxfTrackPayload(id uint32, kind string, scale uint32, samples uint32, dur uint64) []byte {
	b := make([]byte, mxfTrackSetSize)
	p := 0
	binary.BigEndian.PutUint32(b[p:], id)
	p += 4
	copy(b[p:p+4], []byte(kind))
	p += 4
	binary.BigEndian.PutUint32(b[p:], scale)
	p += 4
	binary.BigEndian.PutUint32(b[p:], samples)
	p += 4
	binary.BigEndian.PutUint64(b[p:], dur)
	p += 8
	p += 4                                   // width
	p += 4                                   // height
	binary.BigEndian.PutUint32(b[p:], 48000) // RTP clock rate
	p += 4
	binary.BigEndian.PutUint32(b[p:], 1234) // RTP base
	p += 4
	b[p] = 1 // has anchor
	p++
	binary.BigEndian.PutUint16(b[p:], 1) // primaries
	p += 2
	binary.BigEndian.PutUint16(b[p:], 1) // transfer
	p += 2
	binary.BigEndian.PutUint16(b[p:], 1) // matrix
	return b
}

func TestScanMXFHappyPath(t *testing.T) {
	data := klv(tagHeaderPack, []byte("OP1a"))
	data = append(data, klv(tagTrack, mxfTrackPayload(3, "soun", 48000, 240, 240))...)
	p := writeFile(t, "a.mxf", data)

	meta, err := Scan(p, DefaultLimits())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if meta.Format != "mxf" || meta.MXFOP != "OP1a" || len(meta.Tracks) != 1 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	tr := meta.Tracks[0]
	if tr.ID != 3 || tr.Kind != KindAudio || tr.TimeScale != 48000 ||
		!tr.HasRTPAnchor || tr.RTPClockRate != 48000 || tr.RTPBaseTS != 1234 {
		t.Fatalf("track parse wrong: %+v", tr)
	}
}

func TestScanMXFBERLongForm(t *testing.T) {
	payload := make([]byte, 200)
	key := make([]byte, 16)
	copy(key[0:4], klvPrefix[:])
	binary.BigEndian.PutUint16(key[14:16], tagPreface)
	item := append([]byte{}, key...)
	item = append(item, 0x82, 0x00, 0xC8) // long-form BER: 200
	item = append(item, payload...)
	// Add a valid header + track so the overall scan succeeds.
	full := klv(tagHeaderPack, []byte("OP1a"))
	full = append(full, item...)
	full = append(full, klv(tagTrack, mxfTrackPayload(1, "vide", 24000, 10, 10))...)
	p := writeFile(t, "long.mxf", full)
	meta, err := Scan(p, DefaultLimits())
	if err != nil {
		t.Fatalf("long-form BER must decode: %v", err)
	}
	if meta.BoxCount != 3 {
		t.Fatalf("box count = %d, want 3", meta.BoxCount)
	}
}

func TestScanMXFTruncatedKey(t *testing.T) {
	data := klv(tagHeaderPack, []byte("OP1a"))
	data = data[:len(data)-10] // chop the tail mid-stream
	p := writeFile(t, "trunc.mxf", data)
	_, err := Scan(p, DefaultLimits())
	pe, ok := firstPipelineErr(t, err)
	if !ok || pe.Code != model.CodeTruncatedBox {
		t.Fatalf("want truncated_box, got %v", err)
	}
	if pe.ByteRange.Offset < 0 || pe.ByteRange.Offset > pe.ByteRange.Offset+1 {
		t.Fatal("offset must be non-negative")
	}
}

func TestScanMXFIllegalBER(t *testing.T) {
	key := make([]byte, 16)
	copy(key[0:4], klvPrefix[:])
	binary.BigEndian.PutUint16(key[14:16], tagPreface)
	item := append([]byte{}, key...)
	item = append(item, 0x8F) // long-form width 15: unsupported width
	p := writeFile(t, "badber.mxf", item)
	_, err := Scan(p, DefaultLimits())
	pe, ok := firstPipelineErr(t, err)
	if !ok || pe.Code != model.CodeIllegalLength {
		t.Fatalf("want illegal_length, got %v", err)
	}
}

func TestScanMXFValueOverrun(t *testing.T) {
	key := make([]byte, 16)
	copy(key[0:4], klvPrefix[:])
	binary.BigEndian.PutUint16(key[14:16], tagPreface)
	item := append([]byte{}, key...)
	item = append(item, 0x7F) // claims 127 bytes but none follow
	p := writeFile(t, "overrun.mxf", item)
	_, err := Scan(p, DefaultLimits())
	pe, ok := firstPipelineErr(t, err)
	if !ok || pe.Code != model.CodeTruncatedBox {
		t.Fatalf("want truncated_box for value overrun, got %v", err)
	}
}
