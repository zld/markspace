package fixture

import (
	"bytes"
	"testing"
)

func TestBuildMP4BoxOrder(t *testing.T) {
	data := BuildMP4(MP4Spec{Tracks: []MP4TrackSpec{{
		ID: "a", Kind: "video", StoredWidth: 320, StoredHeight: 240,
		DisplayWidth: 320, DisplayHeight: 240, Timescale: 25,
		SampleCount: 4, SampleDuration: 1,
	}}})
	want := []string{"ftyp", "moov", "mdat"}
	off := 0
	for _, typ := range want {
		if off+8 > len(data) {
			t.Fatalf("truncated before %s", typ)
		}
		if string(data[off+4:off+8]) != typ {
			t.Fatalf("box at %d = %q, want %s", off, data[off+4:off+8], typ)
		}
		size := int(uint32(data[off])<<24 | uint32(data[off+1])<<16 | uint32(data[off+2])<<8 | uint32(data[off+3]))
		if size <= 0 || off+size > len(data) {
			t.Fatalf("%s declares size %d (off %d, len %d)", typ, size, off, len(data))
		}
		off += size
	}
}

func TestBuildMP4TruncateShortens(t *testing.T) {
	spec := MP4Spec{TruncateBytes: 32, Tracks: []MP4TrackSpec{{
		ID: "a", Kind: "video", StoredWidth: 64, StoredHeight: 64,
		Timescale: 25, SampleCount: 2, SampleDuration: 1,
	}}}
	full := BuildMP4(MP4Spec{Tracks: spec.Tracks})
	cut := BuildMP4(spec)
	if len(cut) != len(full)-32 {
		t.Fatalf("truncated len %d, want %d", len(cut), len(full)-32)
	}
}

func TestBuildMP4TimestampSeedDeterministic(t *testing.T) {
	spec := MP4Spec{Tracks: []MP4TrackSpec{{
		ID: "a", Kind: "video", StoredWidth: 64, StoredHeight: 64,
		Timescale: 25, SampleCount: 2, SampleDuration: 1,
	}}}
	if !bytes.Equal(BuildMP4(spec), BuildMP4(spec)) {
		t.Fatal("fixture builds are not byte-stable")
	}
}
