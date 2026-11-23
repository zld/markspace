package ebml

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
)

// el builds an element with explicit size width. A negative size width means
// "use the minimal encoding".
func el(id uint32, payload []byte) []byte {
	width := idWidth(id)
	out := appendID(nil, id, width)
	out = append(out, EncodeSize(uint64(len(payload)), false)...)
	return append(out, payload...)
}

func elUnknown(id uint32, payload []byte) []byte {
	out := appendID(nil, id, idWidth(id))
	out = append(out, 0xFF) // 1-byte unknown size
	return append(out, payload...)
}

func idWidth(id uint32) int {
	switch {
	case id > 0xFFFFFF:
		return 4
	case id > 0xFFFF:
		return 3
	case id > 0xFF:
		return 2
	default:
		return 1
	}
}

func appendID(dst []byte, id uint32, width int) []byte {
	for i := width - 1; i >= 0; i-- {
		dst = append(dst, byte(id>>(8*i)))
	}
	return dst
}

func uints(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte(v)}, b...)
		v >>= 8
	}
	return b
}

func float64Bytes(f float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, mathFloat64bits(f))
	return b
}

func simpleBlock(track uint64, rel int16, flags byte, payload []byte) []byte {
	body := append([]byte{0x80 | byte(track)}, byte(rel>>8), byte(rel), flags)
	return append(body, payload...)
}

// buildDoc assembles a small but structurally complete Matroska document.
func buildDoc(segUnknownSize bool, clusters [][]byte) []byte {
	var doc []byte
	hdr := append(el(IDDocType, []byte("matroska")), el(IDDocTypeVersion, uints(4))...)
	doc = append(doc, el(IDEBML, hdr)...)
	var seg []byte
	info := append(el(IDTimecodeScale, uints(1_000_000)), el(IDDuration, float64Bytes(4000.0))...)
	seg = append(seg, el(IDInfo, info)...)
	tracks := el(IDTrackEntry, append(
		append(el(IDTrackNumber, uints(1)), el(IDTrackType, uints(1))...),
		append(el(IDCodecID, []byte("V_VP9")), el(IDDefaultDuration, uints(33_333_333))...)...,
	))
	seg = append(seg, el(IDTracks, tracks)...)
	for _, c := range clusters {
		seg = append(seg, c...)
	}
	if segUnknownSize {
		doc = append(doc, appendID(nil, IDSegment, idWidth(IDSegment))...)
		doc = append(doc, 0xFF)
		doc = append(doc, seg...)
	} else {
		doc = append(doc, el(IDSegment, seg)...)
	}
	return doc
}

func cluster(known bool, tc uint64, blocks ...[]byte) []byte {
	var body []byte
	body = append(body, el(IDTimestamp, uints(tc))...)
	for _, b := range blocks {
		body = append(body, el(IDSimpleBlock, b)...)
	}
	if known {
		return el(IDCluster, body)
	}
	return elUnknown(IDCluster, body)
}

func TestParseKnownSizes(t *testing.T) {
	doc := buildDoc(false, [][]byte{
		cluster(true, 0, simpleBlock(1, 0, 0x80, []byte("a")), simpleBlock(1, 33, 0x00, []byte("b"))),
		cluster(true, 100, simpleBlock(1, 0, 0x80, []byte("c"))),
	})
	var blocks []Block
	got, err := Parse(bytes.NewReader(doc), DefaultParseOptions(), func(b Block) error {
		blocks = append(blocks, b)
		return nil
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.DocType != "matroska" || got.TimecodeScale != 1_000_000 {
		t.Fatalf("doc header: %+v", got)
	}
	if len(got.Tracks) != 1 || got.Tracks[0].CodecID != "V_VP9" {
		t.Fatalf("tracks: %+v", got.Tracks)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(blocks))
	}
	if blocks[0].Timecode != 0 || blocks[1].Timecode != 33 || blocks[2].Timecode != 100 {
		t.Fatalf("timecodes: %v %v %v", blocks[0].Timecode, blocks[1].Timecode, blocks[2].Timecode)
	}
	if !blocks[0].Keyframe || blocks[1].Keyframe {
		t.Fatalf("keyframes: %v %v", blocks[0].Keyframe, blocks[1].Keyframe)
	}
	if got.ElementCount < 10 {
		t.Fatalf("element count too low: %d", got.ElementCount)
	}
	// Probe spans must point at actual SimpleBlock bytes.
	src := doc[blocks[0].Source.Offset:blocks[0].Source.End()]
	if !bytes.Equal(src[0:1], []byte{0xA3}) {
		t.Fatalf("block span does not start at SimpleBlock id: %x", src[:4])
	}
	if blocks[0].PayloadOff-blocks[0].Source.Offset != int64(1+len(EncodeSize(1+2+1+1, false))+1+2+1) {
		t.Fatalf("unexpected payload offset: %d", blocks[0].PayloadOff)
	}
}

func TestParseUnknownSizeSegmentAndCluster(t *testing.T) {
	doc := buildDoc(true, [][]byte{
		cluster(false, 0, simpleBlock(1, 0, 0x80, []byte("a"))),
		cluster(false, 50, simpleBlock(1, 0, 0x00, []byte("b")), simpleBlock(1, 10, 0x00, []byte("c"))),
	})
	var blocks []Block
	got, err := Parse(bytes.NewReader(doc), DefaultParseOptions(), func(b Block) error {
		blocks = append(blocks, b)
		return nil
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.Segment.UnknownSize {
		t.Fatal("segment should be unknown-size")
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(blocks))
	}
	// Unknown-size clusters must not swallow the next cluster's timestamp.
	if blocks[2].Timecode != 60 {
		t.Fatalf("timecode = %d, want 60", blocks[2].Timecode)
	}
}

func TestParseTruncatedBlockReportsTrackAndOffset(t *testing.T) {
	doc := buildDoc(false, [][]byte{cluster(true, 0, simpleBlock(1, 0, 0x80, []byte("payload")))})
	// Cut into the block payload so the declared SimpleBlock size overruns EOF.
	cut := doc[:len(doc)-3]
	_, err := Parse(bytes.NewReader(cut), DefaultParseOptions(), nil)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("not a ParseError: %v", err)
	}
	if pe.Kind != ErrKindTruncated {
		t.Fatalf("kind = %s, want %s", pe.Kind, ErrKindTruncated)
	}
	if pe.Offset <= 0 {
		t.Fatalf("offset not reported: %d", pe.Offset)
	}
}

func TestParseTruncatedVINTIsCategorized(t *testing.T) {
	doc := buildDoc(false, nil)
	// A lone 0x40 size byte claims a 2-byte size but is cut off.
	bad := append(append([]byte{}, doc...), 0x40)
	_, err := Parse(bytes.NewReader(bad), DefaultParseOptions(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Kind != ErrKindTruncated {
		t.Fatalf("err = %v", err)
	}
}

func TestParseInvalidVINTIsCategorized(t *testing.T) {
	doc := buildDoc(true, [][]byte{cluster(true, 0, simpleBlock(1, 0, 0x80, []byte("a")))})
	// Zero first byte after a complete cluster, still inside the unknown-size
	// segment: not a legal ID.
	bad := append(append([]byte{}, doc...), 0x00, 0x00)
	_, err := Parse(bytes.NewReader(bad), DefaultParseOptions(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Kind != ErrKindInvalidVINT {
		t.Fatalf("kind = %v", pe)
	}
}

func TestSinkErrorPropagates(t *testing.T) {
	doc := buildDoc(false, [][]byte{cluster(true, 0, simpleBlock(1, 0, 0x80, []byte("a")))})
	sentinel := errors.New("stop")
	_, err := Parse(bytes.NewReader(doc), DefaultParseOptions(), func(Block) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}

func TestReaderDigestCoversWholeInput(t *testing.T) {
	doc := buildDoc(false, [][]byte{cluster(true, 0, simpleBlock(1, 0, 0x80, []byte("a")))})
	r := NewReader(bytes.NewReader(doc))
	if _, err := io.Copy(io.Discard, r.br); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	if got := r.Digest(); got != sum {
		t.Fatalf("digest mismatch: %x != %x", got, sum)
	}
}

func mathFloat64bits(f float64) uint64 { return math.Float64bits(f) }
