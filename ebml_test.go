package mediakernel

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	fix := BuildInterleaved(7)
	m, err := Parse(bytes.NewReader(fix.Data), int64(len(fix.Data)), 1<<20)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(m.Registry.Tracks); got != 2 {
		t.Fatalf("tracks = %d, want 2", got)
	}
	if got := len(m.Blocks); got != 64 {
		t.Fatalf("blocks = %d, want 64", got)
	}
	if m.TimecodeScale != 1_000_000 {
		t.Fatalf("timecode scale = %d", m.TimecodeScale)
	}
	trk := m.Registry.Tracks[1]
	if trk.Codec != "RTP/PCM16LE" || trk.DefaultDuration != 20_000_000 {
		t.Fatalf("track 1 = %+v", trk)
	}
}

func TestUnknownSizeEndsAtParentBoundary(t *testing.T) {
	var b builder
	b.buf.Write(masterK(IDEBML, uintLeaf(0x4286, 1)))
	b.openUnknown(IDSegment)
	b.buf.Write(masterU(IDCluster, uintLeaf(IDTimecode, 0),
		leafb(IDSimpleBlock, []byte{0x81, 0, 0, 0, 1, 2, 3})))
	// A level-2 element ends the unknown-size cluster at the parent boundary.
	b.buf.Write(masterK(IDInfo, uintLeaf(IDTimecodeScale, 1_000_000)))
	m, err := Parse(bytes.NewReader(b.buf.Bytes()), b.pos(), 1<<20)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(m.Blocks))
	}
	if m.TimecodeScale != 1_000_000 {
		t.Fatalf("Info after unknown-size cluster not reached, scale = %d", m.TimecodeScale)
	}
}

func TestUnknownSizeEndsAtEOF(t *testing.T) {
	// BuildInterleaved uses an unknown-size Segment terminated by EOF.
	fix := BuildInterleaved(1)
	if _, err := Parse(bytes.NewReader(fix.Data), int64(len(fix.Data)), 1<<20); err != nil {
		t.Fatalf("parse: %v", err)
	}
}

func TestCorruptFixturesReportExactOffsets(t *testing.T) {
	cases := []struct {
		name string
		fix  Fixture
	}{
		{"truncated-vint", BuildTruncatedVINT(7)},
		{"invalid-vint", BuildInvalidVINT(7)},
		{"bad-parent", BuildBadParent(7)},
		{"size-overflow", BuildSizeOverflow(7)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(bytes.NewReader(c.fix.Data), int64(len(c.fix.Data)), 1<<20)
			mkErr, ok := err.(*Error)
			if !ok {
				t.Fatalf("expected *Error, got %v", err)
			}
			if mkErr.Code != c.fix.ErrCode {
				t.Fatalf("code = %s, want %s", mkErr.Code, c.fix.ErrCode)
			}
			diff := mkErr.Offset - c.fix.ErrOffset
			if diff < 0 {
				diff = -diff
			}
			if diff > 1 {
				t.Fatalf("offset = %d, want %d (diff %d)", mkErr.Offset, c.fix.ErrOffset, diff)
			}
			if mkErr.Path == "" {
				t.Fatalf("error path empty")
			}
		})
	}
}

func TestBlockTooLarge(t *testing.T) {
	var b builder
	b.buf.Write(masterK(IDEBML, uintLeaf(0x4286, 1)))
	b.openUnknown(IDSegment)
	b.buf.Write(masterK(IDCluster, uintLeaf(IDTimecode, 0),
		leafb(IDSimpleBlock, bytes.Repeat([]byte{0x81}, 100))))
	_, err := Parse(bytes.NewReader(b.buf.Bytes()), b.pos(), 10)
	mkErr, ok := err.(*Error)
	if !ok || mkErr.Code != ErrBlockTooLarge {
		t.Fatalf("err = %v, want %s", err, ErrBlockTooLarge)
	}
}

func TestTruncatedDataInsideKnownSize(t *testing.T) {
	var b builder
	b.buf.Write(masterK(IDEBML, uintLeaf(0x4286, 1)))
	b.openUnknown(IDSegment)
	b.id(IDCluster)
	b.size(50) // declares 50 bytes, file ends sooner
	b.buf.Write(uintLeaf(IDTimecode, 0))
	// Unknown total length: EOF inside a known-size element is a truncation.
	_, err := Parse(bytes.NewReader(b.buf.Bytes()), -1, 1<<20)
	mkErr, ok := err.(*Error)
	if !ok || mkErr.Code != ErrTruncatedHeader {
		t.Fatalf("err = %v, want %s", err, ErrTruncatedHeader)
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{ErrInvalidVINT, 42, "/Segment", "boom"}
	if !strings.Contains(e.Error(), "EBML_INVALID_VINT") || !strings.Contains(e.Error(), "42") {
		t.Fatalf("Error() = %q", e.Error())
	}
}
