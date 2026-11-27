package mkv

import (
	"bytes"
	"testing"

	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/ebml"
)

// blockPayload builds a minimal SimpleBlock payload: track VINT, signed
// relative timecode, flags, frame bytes.
func blockPayload(track uint64, rel int16, flags byte, frame []byte) []byte {
	out := append(ebml.EncodeVINT(track),
		byte(int32(rel)>>8), byte(rel), flags)
	return append(out, frame...)
}

func buildDoc(t *testing.T, timeScale uint64, clusterBlocks ...struct {
	track uint64
	rel   int16
}) []byte {
	t.Helper()
	// Track entry for track 1.
	tnum := ebml.EncodeElement(ebml.IDTrackNumber, []byte{1})
	tuid := ebml.EncodeElement(0x73C5, []byte{0, 0, 0, 1})
	codec := ebml.EncodeElement(0x86, []byte("A_OPUS"))
	tracks := ebml.EncodeElement(0x1654AE6B,
		ebml.EncodeElement(ebml.IDTrackEntry, append(append(tnum, tuid...), codec...)))
	info := ebml.EncodeElement(ebml.IDInfo,
		ebml.EncodeElement(ebml.IDTimeScale, u64be(timeScale)))
	var cluster []byte
	cluster = append(cluster, ebml.EncodeElement(ebml.IDTimecode, u64be(0))...)
	for _, b := range clusterBlocks {
		cluster = append(cluster,
			ebml.EncodeElement(ebml.IDSimpleBlock,
				blockPayload(b.track, b.rel, 0, []byte{0xAA}))...)
	}
	seg := append(info, tracks...)
	seg = append(seg, ebml.EncodeElement(ebml.IDCluster, cluster)...)
	return ebml.EncodeElement(ebml.IDSegment, seg)
}

func u64be(v uint64) []byte {
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

func TestParseInterleavedTracks(t *testing.T) {
	doc := buildDoc(t, 1_000_000,
		struct {
			track uint64
			rel   int16
		}{1, 0}, struct {
			track uint64
			rel   int16
		}{2, 0}, struct {
			track uint64
			rel   int16
		}{1, 20},
	)
	parsed, err := Parse(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tracks) != 1 || parsed.Tracks[0].CodecID != "A_OPUS" {
		t.Fatalf("tracks = %+v", parsed.Tracks)
	}
	if len(parsed.Blocks) != 3 {
		t.Fatalf("block count = %d", len(parsed.Blocks))
	}
	sorted := parsed.SortedBlocks()
	// Stable ordering: time 0 keeps arrival order (track1 then track2).
	if sorted[0].TrackNumber != 1 || sorted[1].TrackNumber != 2 {
		t.Fatalf("stable sort wrong: %d %d", sorted[0].TrackNumber, sorted[1].TrackNumber)
	}
	if sorted[2].AbsTime != clock.Tick(20_000_000) {
		t.Fatalf("abs time = %d", sorted[2].AbsTime)
	}
	if len(parsed.Anomalies) != 0 {
		t.Fatalf("unexpected anomalies: %+v", parsed.Anomalies)
	}
}

func TestRegressionAndDuplicate(t *testing.T) {
	doc := buildDoc(t, 1_000_000,
		struct {
			track uint64
			rel   int16
		}{1, 100}, struct {
			track uint64
			rel   int16
		}{1, 50}, struct {
			track uint64
			rel   int16
		}{1, 50},
	)
	parsed, err := Parse(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Anomalies) != 2 {
		t.Fatalf("anomaly count = %d: %+v", len(parsed.Anomalies), parsed.Anomalies)
	}
	if parsed.Anomalies[0].Code != clock.CodeTimeRegression {
		t.Fatalf("first anomaly = %s", parsed.Anomalies[0].Code)
	}
	if parsed.Anomalies[1].Code != clock.CodeTimeDuplicate {
		t.Fatalf("second anomaly = %s", parsed.Anomalies[1].Code)
	}
	// Original block timestamps must remain unmodified.
	if parsed.Blocks[1].AbsTime != clock.Tick(50_000_000) {
		t.Fatal("block timestamp was mutated")
	}
	if parsed.Anomalies[0].SourceOffset <= 0 {
		t.Fatal("anomaly missing source offset")
	}
}

func TestSourceOffsetsUnique(t *testing.T) {
	doc := buildDoc(t, 1_000_000,
		struct {
			track uint64
			rel   int16
		}{1, 0}, struct {
			track uint64
			rel   int16
		}{1, 10},
	)
	parsed, err := Parse(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, b := range parsed.Blocks {
		if seen[b.SourceOffset] {
			t.Fatal("duplicate source offset")
		}
		seen[b.SourceOffset] = true
	}
}
