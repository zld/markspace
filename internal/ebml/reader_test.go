package ebml

import (
	"bytes"
	"errors"
	"testing"

	"mediaarchive/internal/timeline"
)

func validFile(t *testing.T, unknownSegment, unknownCluster bool) []byte {
	t.Helper()
	blk1 := SimpleBlock(1, 0, true, []byte("video-frame-0"))
	blk2 := SimpleBlock(2, 250, false, []byte("audio-frame-0"))
	blk3 := SimpleBlock(1, 500, false, []byte("video-frame-1"))
	var c1, c2 []byte
	if unknownCluster {
		c1 = ClusterUnknown(0, blk1, blk2)
		c2 = ClusterUnknown(1000, blk3)
	} else {
		c1 = Cluster(0, blk1, blk2)
		c2 = Cluster(1000, blk3)
	}
	return BuildMatroska([][]byte{c1, c2}, unknownSegment)
}

func TestParseHappyPathKnownLength(t *testing.T) {
	data := validFile(t, false, false)
	r, err := NewReader(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Parse()
	if err != nil {
		t.Fatalf("Parse returned %v", err)
	}
	if len(res.Events) != 3 {
		t.Fatalf("events=%d want 3", len(res.Events))
	}
	tracks := map[uint32]bool{}
	var prev int64 = -1
	for _, ev := range res.Events {
		tracks[ev.Track] = true
		if ev.TimeNanos < prev {
			t.Fatalf("time regression at %d", ev.FileOffset)
		}
		prev = ev.TimeNanos
	}
	if !tracks[1] || !tracks[2] {
		t.Fatalf("missing tracks: %v", tracks)
	}
	if res.Report.BlockCount != 3 || res.Report.FirstBlockNanos != 0 {
		t.Fatalf("report wrong: %+v", res.Report)
	}
	// Relative timecode 250ms inside cluster 0.
	if res.Events[1].TimeNanos != 250_000_000 {
		t.Fatalf("audio timecode=%d", res.Events[1].TimeNanos)
	}
}

func TestParseUnknownLengthSegmentAndCluster(t *testing.T) {
	data := validFile(t, true, true)
	r, _ := NewReader(data, DefaultLimits())
	res, err := r.Parse()
	if err != nil {
		t.Fatalf("unknown-length parse failed: %v", err)
	}
	if len(res.Events) != 3 || res.Segment.Size != -1 {
		t.Fatalf("events=%d segment=%+v", len(res.Events), res.Segment)
	}
}

func TestTruncatedClusterReportsOffsetAndRecovers(t *testing.T) {
	// Build a healthy first cluster, then a second cluster whose declared
	// size promises more bytes than the file actually contains.
	blk1 := SimpleBlock(1, 0, true, []byte("video-frame-0"))
	blk2 := SimpleBlock(2, 250, false, []byte("audio-frame-0"))
	c1 := Cluster(0, blk1, blk2)
	promised := SimpleBlock(1, 500, false, []byte("cut-payload"))
	var c2 bytes.Buffer
	c2.Write(UintElement(IDTimecode, 1000))
	c2.Write(promised)
	rawC2 := c2.Bytes()
	c2Header := append(append([]byte{}, IDCluster...), EncodeVINT(uint64(len(rawC2)+6), 0)...)
	truncC2 := append(c2Header, rawC2...)
	truncC2 = truncC2[:len(truncC2)-6] // drop six payload bytes
	data := BuildMatroska([][]byte{c1, truncC2}, false)

	r, _ := NewReader(data, DefaultLimits())
	res, err := r.Parse()
	if err != nil {
		t.Fatalf("truncation should be recoverable, got %v", err)
	}
	if len(res.Report.TruncatedClusterOffsets) == 0 {
		t.Fatal("expected truncated_cluster_offset in report")
	}
	// All fully readable blocks are retained; only the cluster boundary was
	// broken, so the third (complete) block still survives.
	if len(res.Events) != 3 {
		t.Fatalf("recovered events=%d want 3 intact blocks", len(res.Events))
	}
	if !hasIssue(res.Report, timeline.ErrTruncated) {
		t.Fatal("truncation issue not recorded")
	}
}

func TestTruncatedHeaderIsUnrecoverable(t *testing.T) {
	data := validFile(t, false, false)
	r, _ := NewReader(data[:4], DefaultLimits()) // EBML header id only
	_, err := r.Parse()
	if err == nil || !errors.Is(err, timeline.ErrTruncated) {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func TestOversizedElementRejected(t *testing.T) {
	data := validFile(t, false, false)
	limits := DefaultLimits()
	limits.MaxElementSize = 16 // every real element is bigger
	r, _ := NewReader(data, limits)
	_, err := r.Parse()
	if err == nil || !errors.Is(err, timeline.ErrLimitExceeded) {
		t.Fatalf("expected limit error, got %v", err)
	}
}

func TestFileSizeCapEnforced(t *testing.T) {
	data := validFile(t, false, false)
	limits := DefaultLimits()
	limits.MaxFileSize = int64(len(data) - 1)
	if _, err := NewReader(data, limits); !errors.Is(err, timeline.ErrLimitExceeded) {
		t.Fatalf("expected file cap error, got %v", err)
	}
}

func TestSuperLongEBMLLengthRejected(t *testing.T) {
	// EBML header followed by a segment whose declared size exceeds input.
	data := append(MinimalHeader(), IDSegment...)
	data = append(data, 0x01, 0x00, 0x00, 0x00, 0x00) // 5-byte size header
	data = append(data, 0x1F)                         // start of payload bytes
	r, _ := NewReader(data, DefaultLimits())
	_, err := r.Parse()
	if err == nil || !errors.Is(err, timeline.ErrTruncated) {
		t.Fatalf("declared overrun must be rejected, got %v", err)
	}
}

func TestOutOfBoundsLengthNoReadPastEnd(t *testing.T) {
	// Valid header, then a cluster id with a size claiming 1 MiB.
	cluster := append(append([]byte{}, IDCluster...), 0x41, 0x10)
	cluster = append(cluster, 0x00, 0x00) // size 0x100000
	data := append(MinimalHeader(), Segment(cluster, false)...)
	r, _ := NewReader(data, DefaultLimits())
	res, err := r.Parse()
	// The over-long cluster is a recoverable truncation: no blocks survive,
	// but the parser never reads past EOF and records the fault.
	if err != nil {
		t.Fatalf("recoverable truncation expected nil error, got %v", err)
	}
	if !hasIssue(res.Report, timeline.ErrTruncated) {
		t.Fatalf("expected truncation issue, got %+v", res.Report.Issues)
	}
	if len(res.Events) != 0 {
		t.Fatalf("no events should be recovered from a broken cluster")
	}
}

func TestBlockSizeCap(t *testing.T) {
	// A genuinely complete 256-byte block payload inside an intact cluster.
	payload := bytes.Repeat([]byte{0xAB}, 256)
	cluster := Cluster(0, SimpleBlock(1, 0, true, payload))
	data := BuildMatroska([][]byte{cluster}, false)
	limits := DefaultLimits()
	limits.MaxBlockSize = 64
	r, _ := NewReader(data, limits)
	_, err := r.Parse()
	if !errors.Is(err, timeline.ErrLimitExceeded) {
		t.Fatalf("expected block cap error, got %v", err)
	}
}

func TestEncodeVINTRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 126, 127, 128, 16382, 16383, 70000} {
		enc := EncodeVINT(v, 0)
		dec, n, err := readVINT(enc)
		if err != nil || dec != v || n != len(enc) {
			t.Fatalf("VINT round trip %d -> % x -> %d (%d, %v)", v, enc, dec, n, err)
		}
	}
	// Explicit 1-byte width at its max data value renders the 0xFF marker,
	// which the parser treats as an unknown-length VINT; so known 127 uses
	// two bytes while unknown length uses the canonical four.
	if enc := EncodeVINT(127, 0); len(enc) != 2 {
		t.Fatalf("auto width for 127 = %d bytes, want 2", len(enc))
	}
	unk := EncodeUnknownSize(4)
	if len(unk) != 4 || unk[0] != 0xFF {
		t.Fatalf("unknown marker = % x", unk)
	}
}

func hasIssue(rep *ParseReport, target error) bool {
	for _, is := range rep.Issues {
		if errors.Is(is, target) {
			return true
		}
	}
	return false
}
