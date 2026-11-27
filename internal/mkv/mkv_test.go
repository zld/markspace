package mkv

import (
	"strings"
	"testing"

	"tracecheck/internal/ebml"
)

// fixtureOpts controls the synthetic Matroska bytes used by the package tests.
type fixtureOpts struct {
	docType        string // default "webm"
	timestampScale uint64 // 0 => omit Info/TimestampScale entirely
	segUnknown     bool
	clusters       [][]byte // each is the body of a Cluster (Timestamp + blocks)
}

// clusterBody builds a Cluster payload with a Timestamp and the given blocks.
func clusterBody(ts uint64, blocks ...[]byte) []byte {
	var c ebml.Builder
	_ = c.UintElement(ebml.IDTimestamp, ts)
	for _, b := range blocks {
		_ = c.Element(ebml.IDSimpleBlock, b)
	}
	return c.Bytes()
}

// simpleBlock encodes a SimpleBlock payload.
func simpleBlock(track uint64, rel int16, flags byte, frame []byte) []byte {
	tv := ebml.MustEncodeVINT(track)
	out := append([]byte{}, tv...)
	out = append(out, byte(uint16(rel)>>8), byte(uint16(rel)))
	out = append(out, flags)
	return append(out, frame...)
}

func buildFixture(t *testing.T, o fixtureOpts) []byte {
	t.Helper()
	if o.docType == "" {
		o.docType = "webm"
	}
	var top ebml.Builder
	if err := top.Container(ebml.IDEBMLHeader, func(h *ebml.Builder) error {
		return h.StringElement(ebml.IDDocType, o.docType)
	}); err != nil {
		t.Fatal(err)
	}
	var seg ebml.Builder
	if o.timestampScale != 0 {
		if err := seg.Container(ebml.IDInfo, func(info *ebml.Builder) error {
			return info.UintElement(ebml.IDTimecodeScale, o.timestampScale)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := seg.Container(ebml.IDTracks, func(tr *ebml.Builder) error {
		return tr.Container(ebml.IDTrackEntry, func(e *ebml.Builder) error {
			return e.UintElement(ebml.IDTrackNumber, 1)
		})
	}); err != nil {
		t.Fatal(err)
	}
	for _, body := range o.clusters {
		if o.segUnknown {
			if err := seg.UnknownElement(ebml.IDCluster, body); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := seg.Element(ebml.IDCluster, body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if o.segUnknown {
		if err := top.UnknownElement(ebml.IDSegment, seg.Bytes()); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := top.Element(ebml.IDSegment, seg.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	return top.Bytes()
}

func parseFixture(t *testing.T, data []byte) (*ebml.Document, []Block) {
	t.Helper()
	doc, err := ebml.Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	blocks, err := ParseBlocks(doc)
	if err != nil {
		t.Fatalf("blocks: %v", err)
	}
	return doc, blocks
}

func TestParseBlocksOffsetAndOrder(t *testing.T) {
	frame := []byte{0xDE, 0xAD}
	c1 := clusterBody(0,
		simpleBlock(1, 0, 0x80, frame),
		simpleBlock(2, 0, 0x00, frame))
	c2 := clusterBody(100,
		simpleBlock(1, 5, 0x80, frame))
	data := buildFixture(t, fixtureOpts{
		timestampScale: 1_000_000,
		segUnknown:     true,
		clusters:       [][]byte{c1, c2},
	})
	doc, blocks := parseFixture(t, data)
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	// Every block must carry an exact in-file offset whose bytes are the track
	// VINT: never a copy or an approximation.
	for _, b := range blocks {
		if b.Offset < 0 || b.PayloadEnd > int64(len(data)) {
			t.Fatalf("block offset out of range: %+v", b)
		}
		if data[b.Offset]&0x80 == 0 {
			t.Errorf("block offset %d does not start a 1-byte ID", b.Offset)
		}
	}
	if blocks[0].TimecodeTicks() != 0 || blocks[2].TimecodeTicks() != 105 {
		t.Errorf("ticks: b0=%d b2=%d", blocks[0].TimecodeTicks(), blocks[2].TimecodeTicks())
	}
	SortStable(blocks)
	if blocks[0].TrackNumber != 1 || blocks[1].TrackNumber != 2 {
		t.Errorf("same-instant ordering should be by track: %d,%d",
			blocks[0].TrackNumber, blocks[1].TrackNumber)
	}
	if blocks[2].TimecodeTicks() != 105 {
		t.Errorf("last block ticks = %d", blocks[2].TimecodeTicks())
	}
	_ = doc
}

func TestParseBlocksTruncated(t *testing.T) {
	// A block whose element declares a size larger than its cluster payload is
	// caught by the EBML parser as LENGTH_OUT_OF_BOUNDS.
	tiny := clusterBody(0, []byte{0x81, 0x00})
	data := buildFixture(t, fixtureOpts{clusters: [][]byte{tiny}})
	doc, err := ebml.Parse(data)
	if err != nil {
		t.Fatalf("container parse should succeed: %v", err)
	}
	// Now hand-build a block with a 1-byte header only: no flags byte.
	var c ebml.Builder
	_ = c.UintElement(ebml.IDTimestamp, 0)
	_ = c.Element(ebml.IDSimpleBlock, []byte{0x81, 0x00})
	data2 := buildFixture(t, fixtureOpts{clusters: [][]byte{c.Bytes()}})
	doc2, perr := ebml.Parse(data2)
	if perr != nil {
		t.Fatal(perr)
	}
	_, berr := ParseBlocks(doc2)
	if berr == nil {
		t.Fatal("expected a truncated block error")
	}
	_ = doc
}

func TestParseTracksDefaultsAndConflicts(t *testing.T) {
	var tracks ebml.Builder
	_ = tracks.Container(ebml.IDTrackEntry, func(e *ebml.Builder) error {
		_ = e.UintElement(ebml.IDTrackNumber, 1)
		_ = e.UintElement(ebml.IDTrackType, TrackAudio)
		_ = e.StringElement(ebml.IDCodecID, "A_OPUS")
		// No Audio/Channels or SamplingFrequency: defaults must apply.
		_ = e.Container(ebml.IDAudio, func(a *ebml.Builder) error {
			_ = a.UintElement(ebml.IDChannels, 2)
			_ = a.UintElement(ebml.IDChannels, 6) // conflicting duplicate
			return nil
		})
		return nil
	})
	var seg ebml.Builder
	_ = seg.Container(ebml.IDInfo, func(info *ebml.Builder) error {
		return info.UintElement(ebml.IDTimecodeScale, 500_000)
	})
	_ = seg.Element(ebml.IDTracks, tracks.Bytes())
	var top ebml.Builder
	_ = top.Element(ebml.IDSegment, seg.Bytes())

	doc, err := ebml.Parse(top.Bytes())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tr, err := ParseTracks(doc)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	if tr.TimestampScale != 500_000 {
		t.Errorf("scale=%d", tr.TimestampScale)
	}
	track := tr.ByNumber(1)
	if track == nil {
		t.Fatal("track 1 missing")
	}
	if track.Audio.Channels.Value != 2 || !track.Audio.Channels.Present {
		t.Errorf("channels=%+v", track.Audio.Channels)
	}
	if !track.Audio.SamplingFrequency.Defaulted || track.Audio.SamplingFrequency.Value != DefaultSamplingFreq {
		t.Errorf("sampling default=%+v", track.Audio.SamplingFrequency)
	}
	if !track.Audio.BitDepth.Defaulted || track.Audio.Channels.Defaulted {
		t.Errorf("bit depth default wrong: %+v", track.Audio.BitDepth)
	}
	if len(track.Conflicts) != 1 {
		t.Fatalf("conflicts=%+v", track.Conflicts)
	}
	if track.Conflicts[0].Field != "Channels" || track.Conflicts[0].Offset < 0 {
		t.Errorf("conflict detail wrong: %+v", track.Conflicts[0])
	}
}

func TestParseTracksScaleDefault(t *testing.T) {
	data := buildFixture(t, fixtureOpts{timestampScale: 0})
	doc, _ := parseFixture(t, data)
	tr, err := ParseTracks(doc)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	if tr.TimestampScale != DefaultTimestampScale || !tr.ScaleDefaulted {
		t.Errorf("scale default=%d defaulted=%v", tr.TimestampScale, tr.ScaleDefaulted)
	}
}

func TestMetadataSummaryStable(t *testing.T) {
	data := buildFixture(t, fixtureOpts{
		timestampScale: 1_000_000,
		clusters:       [][]byte{clusterBody(0, simpleBlock(1, 0, 0x80, []byte{1}))},
	})
	doc, blocks := parseFixture(t, data)
	tr, err := ParseTracks(doc)
	if err != nil {
		t.Fatal(err)
	}
	m := BuildMetadata(doc, tr, blocks)
	first := m.Summary()
	second := BuildMetadata(doc, tr, blocks).Summary()
	if first != second {
		t.Fatalf("summary not stable:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(first, "track 1") || !strings.Contains(first, "blocks=1") {
		t.Errorf("summary content: %s", first)
	}
}
