package ebml

import (
	"bytes"
	"testing"
)

func TestReadMetadata(t *testing.T) {
	raw, err := buildMetaStream()
	if err != nil {
		t.Fatal(err)
	}
	seg, err := OpenSegment(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	scale, tracks, err := seg.ReadMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if scale != 1_000_000 {
		t.Fatalf("timestamp scale=%d want 1000000", scale)
	}
	if len(tracks) != 2 {
		t.Fatalf("track count=%d want 2", len(tracks))
	}
	if tracks[0].Number != 1 || tracks[0].CodecID != "V_VP9" || tracks[0].Type != 1 {
		t.Fatalf("track 0 wrong: %+v", tracks[0])
	}
	if tracks[1].Number != 2 || tracks[1].CodecID != "A_OPUS" || tracks[1].Type != 2 {
		t.Fatalf("track 1 wrong: %+v", tracks[1])
	}
	if tracks[0].HeaderStart < 0 {
		t.Fatal("track entry must retain its header offset")
	}
	// Reading metadata must rewind the segment reader so cluster scanning
	// still works afterwards.
	clusters := 0
	if err := seg.ScanClusters(func(c *Cluster) error {
		clusters++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if clusters != 1 {
		t.Fatalf("clusters after metadata read=%d want 1", clusters)
	}
}

// buildMetaStream reuses the fixture-style manual encoding to keep this test
// independent of the fixture package.
func buildMetaStream() ([]byte, error) {
	var seg []byte
	// Info with default timestamp scale.
	seg = append(seg, wrapMeta(IDInfoElement, uintMeta(IDTimestampScale, 1_000_000))...)
	// Two track entries.
	var tracks []byte
	for i, codec := range []string{"V_VP9", "A_OPUS"} {
		num := uint64(i + 1)
		ttype := uint64(2)
		if i == 0 {
			ttype = 1
		}
		entry := uintMeta(IDTrackNumberEl, num)
		entry = append(entry, uintMeta(IDTrackUIDEl, 100+num)...)
		entry = append(entry, uintMeta(IDTrackTypeEl, ttype)...)
		entry = append(entry, strMeta(IDCodecIDEl, codec)...)
		tracks = append(tracks, wrapMeta(IDTrackEntryEl, entry)...)
	}
	seg = append(seg, wrapMeta(IDTracksElement, tracks)...)
	// One cluster with a single block so ScanClusters has something to find.
	blk := []byte{0x81, 0x00, 0x00, 0x80, 'd', 'a', 't', 'a'}
	body := append(uintMeta(IDTimestamp, 0), wrapMeta(IDSimpleBlock, blk)...)
	seg = append(seg, wrapMeta(IDCluster, body)...)

	out := wrapMeta(IDEBMLHeader, []byte{})
	out = append(out, wrapMeta(IDSegment, seg)...)
	return out, nil
}

func wrapMeta(id uint32, payload []byte) []byte {
	out := append(idMeta(id), sizeMeta(uint64(len(payload)))...)
	return append(out, payload...)
}

func idMeta(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

func sizeMeta(v uint64) []byte {
	for width := 1; width <= 8; width++ {
		if v < (uint64(1)<<(7*width))-1 {
			out := make([]byte, width)
			val := v | (uint64(1) << (7 * width))
			for i := width - 1; i >= 0; i-- {
				out[i] = byte(val >> (8 * (width - 1 - i)))
			}
			return out
		}
	}
	panic("size overflow")
}

func uintMeta(id uint32, v uint64) []byte {
	var raw []byte
	if v == 0 {
		raw = []byte{0}
	} else {
		for x := v; x > 0; x >>= 8 {
			raw = append([]byte{byte(x)}, raw...)
		}
	}
	return wrapMeta(id, raw)
}

func strMeta(id uint32, s string) []byte { return wrapMeta(id, []byte(s)) }
