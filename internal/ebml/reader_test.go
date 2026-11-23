package ebml

import (
	"errors"
	"testing"
)

func TestVINTRoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 126, 127, 128, 16382, 1 << 20, 1 << 40}
	for _, v := range cases {
		enc := EncodeVINT(v, 0)
		got, width, err := readVINT(enc, 0, int64(len(enc)))
		if err != nil {
			t.Fatalf("value %d: unexpected error %v", v, err)
		}
		if got != v || width != len(enc) {
			t.Fatalf("value %d: got %d width %d, want %d/%d", v, got, width, v, len(enc))
		}
	}
}

func TestUnknownSizeVINT(t *testing.T) {
	// TrackEntry (0xAE) is a single-byte ID whose VINT value round-trips.
	raw := ElementBytesUnknown(IDTrackEntry, []byte{1, 2, 3})
	r := NewReader(raw, 0)
	el, err := r.Next()
	if err != nil {
		t.Fatalf("parse unknown-size element: %v", err)
	}
	if el.Size != UnknownSize || el.ID != IDTrackEntry || el.Header != 2 {
		t.Fatalf("unexpected element: %+v", el)
	}
	if len(el.Payload) != 3 {
		t.Fatalf("payload should run to buffer end, got %d bytes", len(el.Payload))
	}
}

func TestTruncatedVINT(t *testing.T) {
	// Marker promises four bytes but only two follow.
	data := []byte{0x11, 0x22}
	_, _, err := readVINT(data, 0, int64(len(data)))
	var pe *Error
	if !errors.As(err, &pe) || pe.Category != CategoryTruncated {
		t.Fatalf("want truncated structured error, got %v", err)
	}
}

func TestOutOfRangeLengthNeverReadsPast(t *testing.T) {
	var raw []byte
	raw = append(raw, EncodeID(IDCluster)...)
	raw = append(raw, EncodeVINT(4096, 0)...) // claims 4 KiB payload
	raw = append(raw, 0xAA, 0xBB)             // only 2 bytes present
	r := NewReader(raw, 0)
	_, err := r.Next()
	var pe *Error
	if !errors.As(err, &pe) || pe.Category != CategoryOutOfRange {
		t.Fatalf("want out-of-range error, got %v", err)
	}
	if pe.Offset != 0 {
		t.Fatalf("offset should point at element start, got %d", pe.Offset)
	}
}

func TestTruncatedClusterKeepsCompleteBlocks(t *testing.T) {
	scale := int64(1_000_000)
	good := simpleBlock(1, 0, true, []byte{1, 2, 3, 4})
	good2 := simpleBlock(2, 20, false, []byte{5, 6})
	partial := simpleBlock(1, 40, false, []byte{7, 8, 9, 10, 11, 12})
	tc := ElementBytes(IDTimecode, UintBytes(100))
	var payload []byte
	payload = append(payload, tc...)
	payload = append(payload, good...)
	payload = append(payload, good2...)
	payload = append(payload, partial...)
	// Unknown-size cluster cut inside the third block: the two complete
	// blocks survive, the partial block is recorded and discarded.
	cluster := ElementBytesUnknown(IDCluster, payload)
	cut := len(cluster) - 3
	doc := ParseDocument(cluster[:cut])
	if len(doc.Clusters) != 1 {
		t.Fatalf("want 1 cluster, got %d", len(doc.Clusters))
	}
	c := doc.Clusters[0]
	if len(c.Blocks) != 2 {
		t.Fatalf("the two complete blocks must be retained, got %d", len(c.Blocks))
	}
	if !c.Truncated || len(c.Errors) == 0 {
		t.Fatalf("truncation must be recorded, got truncated=%v errors=%d",
			c.Truncated, len(c.Errors))
	}
	wantTc := int64(100) * scale
	if c.Blocks[0].TimecodeNs != wantTc {
		t.Fatalf("block timecode = %d, want %d", c.Blocks[0].TimecodeNs, wantTc)
	}
	if c.Blocks[1].TimecodeNs != (100+20)*scale {
		t.Fatalf("relative timecode not applied: %d", c.Blocks[1].TimecodeNs)
	}
}

func TestMalformedBlockIsDiscarded(t *testing.T) {
	tc := ElementBytes(IDTimecode, UintBytes(0))
	bad := ElementBytes(IDSimpleBlock, []byte{0x81}) // header cannot be complete
	payload := append(tc, bad...)
	cluster := ElementBytes(IDCluster, payload)
	doc := ParseDocument(cluster)
	c := doc.Clusters[0]
	if len(c.Blocks) != 0 {
		t.Fatalf("malformed block must not become a sample, got %d", len(c.Blocks))
	}
	var found bool
	for _, e := range c.Errors {
		if e.Category == CategoryBadBlock {
			found = true
		}
	}
	if !found {
		t.Fatalf("malformed block error not recorded")
	}
}

// simpleBlock builds a SimpleBlock payload: track VINT, int16 relative
// timecode, flags byte, and opaque frame data.
func simpleBlock(track uint32, relTc int16, keyframe bool, data []byte) []byte {
	var p []byte
	p = append(p, EncodeVINT(uint64(track), 0)...)
	p = append(p, byte(relTc>>8), byte(relTc))
	var flags byte
	if keyframe {
		flags = 0x80
	}
	p = append(p, flags)
	p = append(p, data...)
	return ElementBytes(IDSimpleBlock, p)
}
