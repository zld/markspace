package ebml

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"mediaqc/internal/timeline"
)

// encID appends an element ID (marker bits kept).
func encID(buf *bytes.Buffer, id uint32) {
	switch {
	case id > 0xFFFFFF:
		buf.WriteByte(byte(id >> 24))
		fallthrough
	case id > 0xFFFF:
		buf.WriteByte(byte(id >> 16))
		fallthrough
	case id > 0xFF:
		buf.WriteByte(byte(id >> 8))
	}
	buf.WriteByte(byte(id))
}

// encSize appends a known size in minimal vint form.
func encSize(buf *bytes.Buffer, size int) {
	switch {
	case size < 0x7F:
		buf.WriteByte(0x80 | byte(size))
	case size < 0x3FFF:
		buf.WriteByte(0x40 | byte(size>>8))
		buf.WriteByte(byte(size))
	default:
		buf.WriteByte(0x20 | byte(size>>24))
		buf.WriteByte(byte(size >> 16))
		buf.WriteByte(byte(size >> 8))
		buf.WriteByte(byte(size))
	}
}

// encUnknownSize appends a 4-byte unknown size.
func encUnknownSize(buf *bytes.Buffer) {
	buf.Write([]byte{0x1F, 0xFF, 0xFF, 0xFF})
}

func elem(id uint32, payload []byte) []byte {
	var b bytes.Buffer
	encID(&b, id)
	encSize(&b, len(payload))
	b.Write(payload)
	return b.Bytes()
}

func elemUnknown(id uint32, payload []byte) []byte {
	var b bytes.Buffer
	encID(&b, id)
	encUnknownSize(&b)
	b.Write(payload)
	return b.Bytes()
}

func uintElem(id uint32, v uint64) []byte {
	var p bytes.Buffer
	switch {
	case v > 0xFFFFFF:
		p.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	case v > 0xFFFF:
		p.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)})
	case v > 0xFF:
		p.Write([]byte{byte(v >> 8), byte(v)})
	default:
		p.WriteByte(byte(v))
	}
	return elem(id, p.Bytes())
}

// simpleBlock builds a SimpleBlock element for track (<=127), timecode
// and payload.
func simpleBlock(track byte, tc int16, keyframe bool, payload []byte) []byte {
	var p bytes.Buffer
	p.WriteByte(0x80 | track)
	var tcb [2]byte
	binary.BigEndian.PutUint16(tcb[:], uint16(tc))
	p.Write(tcb[:])
	if keyframe {
		p.WriteByte(0x80)
	} else {
		p.WriteByte(0x00)
	}
	p.Write(payload)
	return elem(IDSimpleBlock, p.Bytes())
}

// buildFile assembles a minimal WebM-ish stream: EBML header, then a
// Segment (unknown size if segUnknown) containing the given children.
func buildFile(segUnknown bool, children ...[]byte) []byte {
	var seg bytes.Buffer
	for _, c := range children {
		seg.Write(c)
	}
	var out bytes.Buffer
	out.Write(elem(IDEBML, []byte{0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}))
	if segUnknown {
		out.Write(elemUnknown(IDSegment, seg.Bytes()))
	} else {
		out.Write(elem(IDSegment, seg.Bytes()))
	}
	return out.Bytes()
}

func infoChild() []byte { return elem(IDInfo, uintElem(IDTimestampScale, 1_000_000)) }

func tracksChild() []byte {
	var entries bytes.Buffer
	mk := func(num, typ uint64, codec string) {
		var e bytes.Buffer
		e.Write(uintElem(IDTrackNumber, num))
		e.Write(uintElem(IDTrackType, typ))
		e.Write(elem(IDCodecID, []byte(codec)))
		entries.Write(elem(IDTrackEntry, e.Bytes()))
	}
	mk(1, 1, "V_VP9")
	mk(2, 2, "A_OPUS")
	return elem(IDTracks, entries.Bytes())
}

func clusterChild(ts uint64, unknown bool, blocks ...[]byte) []byte {
	var c bytes.Buffer
	c.Write(uintElem(IDTimestamp, ts))
	for _, b := range blocks {
		c.Write(b)
	}
	if unknown {
		return elemUnknown(IDCluster, c.Bytes())
	}
	return elem(IDCluster, c.Bytes())
}

func TestScanNormal(t *testing.T) {
	data := buildFile(true, infoChild(), tracksChild(),
		clusterChild(0, true,
			simpleBlock(1, 0, true, []byte("frame-v0")),
			simpleBlock(2, 1, false, []byte("frame-a0")),
			simpleBlock(1, 33, false, []byte("frame-v1")),
		),
		clusterChild(66, false,
			simpleBlock(1, 0, true, []byte("frame-v2")),
		),
	)
	res, err := Scan(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if res.TimestampScale != 1_000_000 {
		t.Fatalf("scale = %d", res.TimestampScale)
	}
	if len(res.Tracks) != 2 || res.Tracks[0].Codec != "V_VP9" || res.Tracks[1].Type != 2 {
		t.Fatalf("tracks = %+v", res.Tracks)
	}
	if len(res.Blocks) != 4 {
		t.Fatalf("blocks = %d, want 4", len(res.Blocks))
	}
	if res.Blocks[2].RawTimecode != 33 || res.Blocks[3].RawTimecode != 66 {
		t.Fatalf("timecodes wrong: %+v", res.Blocks)
	}
	if !res.Blocks[0].Keyframe || res.Blocks[1].Keyframe {
		t.Fatalf("keyframe flags wrong")
	}
	// Every recorded offset must point back at a parseable element.
	for _, b := range res.Blocks {
		el, err := ReadElement(bytes.NewReader(data), b.Offset, int64(len(data)))
		if err != nil {
			t.Fatalf("block offset %d unreadable: %v", b.Offset, err)
		}
		if el.ID != IDSimpleBlock {
			t.Fatalf("offset %d: id 0x%x", b.Offset, el.ID)
		}
	}
}

func TestScanTruncatedUnknownSize(t *testing.T) {
	full := buildFile(true, infoChild(),
		clusterChild(0, true, simpleBlock(1, 0, true, make([]byte, 64))))
	trunc := full[:len(full)-10] // cut inside the block payload
	_, err := Scan(bytes.NewReader(trunc), int64(len(trunc)))
	f := timeline.AsFault(err)
	if f == nil || f.Class != timeline.ClassTruncated {
		t.Fatalf("want truncated fault, got %v", err)
	}
	if f.Offset <= 0 || f.Offset >= int64(len(full)) {
		t.Fatalf("fault offset %d out of range", f.Offset)
	}
}

func TestScanTruncatedClusterBoundary(t *testing.T) {
	full := buildFile(false, infoChild(),
		clusterChild(0, false,
			simpleBlock(1, 0, true, []byte("aaaa")),
			simpleBlock(2, 5, false, []byte("bbbb")),
		))
	trunc := full[:len(full)-6] // cut inside the second block
	_, err := Scan(bytes.NewReader(trunc), int64(len(trunc)))
	f := timeline.AsFault(err)
	if f == nil || f.Class != timeline.ClassTruncated {
		t.Fatalf("want truncated fault, got %v", err)
	}
}

func TestScanSizeOverflow(t *testing.T) {
	var b bytes.Buffer
	b.Write(elem(IDEBML, []byte{1}))
	encID(&b, IDSegment)
	encSize(&b, 100)
	// A child claiming an absurd size (5-byte vint, huge value).
	encID(&b, IDCluster)
	b.Write([]byte{0x10, 0x40, 0x00, 0x00, 0x00}) // 2^30 payload bytes
	_, err := Scan(bytes.NewReader(b.Bytes()), int64(b.Len()))
	f := timeline.AsFault(err)
	if f == nil || (f.Class != timeline.ClassTruncated && f.Class != timeline.ClassSizeOverflow) {
		t.Fatalf("want positioned size fault, got %v", err)
	}
}

func TestIndexBinding(t *testing.T) {
	data := buildFile(true, infoChild(), tracksChild(),
		clusterChild(0, true, simpleBlock(1, 0, true, []byte("x"))))
	res, err := Scan(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256(data)
	idx := Build(res, sha)
	if len(idx.Keyframes()) != 1 {
		t.Fatalf("keyframes = %d", len(idx.Keyframes()))
	}
	path := t.TempDir() + "/x.qcindex"
	if err := SaveIndex(path, idx); err != nil {
		t.Fatal(err)
	}
	got, err := LoadIndex(path, sha)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checksum != idx.Checksum {
		t.Fatalf("checksum changed across round-trip")
	}
	// Digest mismatch must be rejected as index_mismatch.
	other := sha256.Sum256([]byte("other input"))
	_, err = LoadIndex(path, other)
	if f := timeline.AsFault(err); f == nil || f.Class != timeline.ClassIndexMismatch {
		t.Fatalf("want index_mismatch, got %v", err)
	}
	// Version drift must be rejected too.
	stale := *idx
	stale.Version = IndexVersion + 1
	stale.Checksum = stale.computeChecksum()
	if err := SaveIndex(path, &stale); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadIndex(path, sha); timeline.AsFault(err).Class != timeline.ClassIndexMismatch {
		t.Fatalf("want index_mismatch on version drift, got %v", err)
	}
}
