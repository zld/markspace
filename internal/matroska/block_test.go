package matroska

import (
	"testing"

	"continuity/internal/cerr"
	"continuity/internal/ebml"
	"continuity/internal/fixture"
)

func TestParseFixture(t *testing.T) {
	in := fixture.Generate(17, "")
	seg, err := Parse(in.Matroska)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(seg.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(seg.Tracks))
	}
	if seg.Tracks[0].Type != 1 || seg.Tracks[1].Type != 2 {
		t.Fatalf("track types = %d,%d", seg.Tracks[0].Type, seg.Tracks[1].Type)
	}
	if seg.Tracks[1].Channels != 2 || seg.Tracks[1].SampleRate != 48000 {
		t.Fatalf("audio params = %d ch %f Hz", seg.Tracks[1].Channels, seg.Tracks[1].SampleRate)
	}
	if !seg.Tracks[0].Colour.Present || seg.Tracks[0].Colour.Matrix != 1 {
		t.Fatalf("colour = %+v", seg.Tracks[0].Colour)
	}
	if len(seg.Blocks) != 121 {
		t.Fatalf("blocks = %d, want 121", len(seg.Blocks))
	}
	// Sorted by (timestamp, track, index); 1 tick = 1 ms = 1e6 ns.
	for i := 1; i < len(seg.Blocks); i++ {
		a, b := seg.Blocks[i-1], seg.Blocks[i]
		if a.TimestampNs > b.TimestampNs {
			t.Fatalf("blocks not sorted at %d: %d > %d", i, a.TimestampNs, b.TimestampNs)
		}
	}
	if seg.Blocks[0].TimestampNs != 0 || seg.Blocks[0].TrackNumber != 1 {
		t.Fatalf("first block = %+v", seg.Blocks[0])
	}
	if seg.Blocks[1].TimestampNs != 5_000_000 || seg.Blocks[1].TrackNumber != 2 {
		t.Fatalf("second block = %+v", seg.Blocks[1])
	}
}

func TestParseTruncatedBlock(t *testing.T) {
	in := fixture.Generate(17, "truncated-block")
	seg, err := Parse(in.Matroska)
	if !cerr.Is(err, cerr.CodeTruncatedData) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeTruncatedData)
	}
	ce := err.(*cerr.Error)
	if ce.Path != "/Segment/Cluster/SimpleBlock" {
		t.Fatalf("path = %q", ce.Path)
	}
	if ce.Offset <= 0 {
		t.Fatalf("offset = %d", ce.Offset)
	}
	// The parseable prefix is preserved.
	if len(seg.Blocks) != 120 {
		t.Fatalf("recovered blocks = %d, want 120", len(seg.Blocks))
	}
}

func TestParseUnknownLengthBoundary(t *testing.T) {
	in := fixture.Generate(17, "unknown-length-boundary")
	seg, err := Parse(in.Matroska)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(seg.Blocks) != 121 {
		t.Fatalf("blocks = %d, want 121", len(seg.Blocks))
	}
}

func TestLacingRejected(t *testing.T) {
	// SimpleBlock with Xiph lacing (flags 0x02).
	block := []byte{0x81, 0x00, 0x00, 0x02, 0xAA}
	sb := append(elem(ebml.IDSimpleBlock), size(len(block))...)
	sb = append(sb, block...)
	cluster := append(elem(ebml.IDCluster), size(len(sb))...)
	cluster = append(cluster, sb...)
	doc := segmentWith(nil, cluster)
	_, err := Parse(doc)
	if !cerr.Is(err, cerr.CodeLacingUnsupported) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeLacingUnsupported)
	}
}

func TestTimestampOverflow(t *testing.T) {
	// TimecodeScale of 2^32 with a huge cluster timecode overflows ns.
	scale := elem(ebml.IDTimecodeScale)
	scale = append(scale, size(4)...)
	scale = append(scale, 0xFF, 0xFF, 0xFF, 0xFF)
	tc := elem(ebml.IDTimecode)
	tc = append(tc, size(8)...)
	tc = append(tc, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	block := []byte{0x81, 0x00, 0x01, 0x80, 0xAA}
	info := append(elem(ebml.IDInfo), append(size(len(scale)), scale...)...)
	clusterBody := append(tc, elem(ebml.IDSimpleBlock)...)
	clusterBody = append(clusterBody, size(len(block))...)
	clusterBody = append(clusterBody, block...)
	cluster := append(elem(ebml.IDCluster), append(size(len(clusterBody)), clusterBody...)...)
	doc := segmentWith(nil, append(info, cluster...))
	_, err := Parse(doc)
	if !cerr.Is(err, cerr.CodeTimestampOverflow) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeTimestampOverflow)
	}
}

// --- tiny EBML builders for hand-crafted docs ---

func elem(id uint32) []byte {
	switch {
	case id > 0xFFFFFF:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFF:
		return []byte{byte(id >> 8), byte(id)}
	}
	return []byte{byte(id)}
}

func size(n int) []byte {
	switch {
	case n < 127:
		return []byte{0x80 | byte(n)}
	case n < 16383:
		return []byte{0x40 | byte(n>>8), byte(n)}
	default:
		return []byte{0x20 | byte(n>>16), byte(n >> 8), byte(n)}
	}
}

func segmentWith(head, body []byte) []byte {
	seg := append(elem(ebml.IDSegment), size(len(body))...)
	seg = append(seg, body...)
	return append(head, seg...)
}
