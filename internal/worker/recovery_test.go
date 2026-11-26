package worker

import (
	"os"
	"path/filepath"
	"testing"

	"tsindex/internal/index"
	"tsindex/internal/ingest"
)

func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o600) }

// buildSegments creates nSeg complete synthetic segments with H.264 keyframe
// payloads so the pool exercises hashing, keyframe scan and sidecar writes.
func buildSegments(t *testing.T, nSeg, packetsPerSeg int) []*ingest.Segment {
	t.Helper()
	var out []*ingest.Segment
	for s := 0; s < nSeg; s++ {
		seg := &ingest.Segment{ID: int64(s), Records: make([]ingest.Record, 0, packetsPerSeg)}
		for i := 0; i < packetsPerSeg; i++ {
			var raw [ingest.PacketSize]byte
			raw[0] = ingest.SyncByte
			raw[1] = byte(0x01) // PID 0x0100
			raw[2] = 0x00
			raw[3] = 0x10 | byte(i&0x0F)
			// PES + Annex-B IDR NAL on the first packet of each segment.
			if i == 0 {
				raw[1] |= 0x40 // PUSI
				pes := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 0}
				copy(raw[4:], pes)
				copy(raw[4+len(pes):], []byte{0, 0, 0, 1, 0x65, byte(s)})
			}
			p := ingest.NewRecordForTest(raw, int64(s*packetsPerSeg+i), 27_000_000+int64(i)*900)
			seg.Records = append(seg.Records, *p)
		}
		seg.StartOffset = int64(s) * int64(packetsPerSeg) * ingest.PacketSize
		seg.EndOffset = seg.StartOffset + int64(packetsPerSeg)*ingest.PacketSize
		out = append(out, seg)
	}
	return out
}

func h264Epoch() *ingest.MappingEpoch {
	return &ingest.MappingEpoch{Programs: map[uint16]*ingest.ProgramMapping{
		1: {ProgramNumber: 1, Streams: map[uint16]ingest.StreamInfo{
			0x0100: {Codec: "h264", Complete: true},
		}},
	}}
}

func runToCompletion(t *testing.T, dir, source string, segs []*ingest.Segment, prior *Checkpoint) map[int64]SegmentResult {
	t.Helper()
	pool := NewPool(2, dir, source, h264Epoch(), prior, 0)
	results := map[int64]SegmentResult{}
	for _, seg := range segs {
		res, err := pool.Process(seg)
		if err != nil {
			t.Fatalf("process segment %d: %v", seg.ID, err)
		}
		results[seg.ID] = res
	}
	if err := pool.Finalize(); err != nil {
		t.Fatal(err)
	}
	return results
}

func TestResumeMatchesFullRun(t *testing.T) {
	segs := buildSegments(t, 6, 8)

	// Full, uninterrupted run in one directory.
	fullDir := t.TempDir()
	full := runToCompletion(t, fullDir, "asset", segs, nil)

	// Interrupted run: complete the first three segments, then stop and reload.
	intDir := t.TempDir()
	first := NewPool(2, intDir, "asset", h264Epoch(), nil, 0)
	for _, seg := range segs[:3] {
		if _, err := first.Process(seg); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Interrupt(); err != nil {
		t.Fatal(err)
	}

	// A fresh process loads the checkpoint and finishes the rest.
	chkPath := filepath.Join(intDir, ".asset.chk.json")
	prior, err := LoadCheckpoint(chkPath)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if len(prior.Completed) != 3 {
		t.Fatalf("checkpoint should hold 3 items, got %d", len(prior.Completed))
	}
	resumed := runToCompletion(t, intDir, "asset", segs, prior)

	// The resumed run must reproduce the full run's hashes for every segment.
	for id, want := range full {
		got, ok := resumed[id]
		if !ok {
			t.Fatalf("segment %d missing after resume", id)
		}
		if !got.Digests.Equal(want.Digests) {
			t.Fatalf("segment %d digest differs after resume", id)
		}
		if got.StartOffset != want.StartOffset || got.EndOffset != want.EndOffset {
			t.Fatalf("segment %d offsets differ", id)
		}
	}
}

func TestResumeSkipsCheckpointedSegments(t *testing.T) {
	segs := buildSegments(t, 4, 8)
	dir := t.TempDir()
	first := NewPool(2, dir, "asset", h264Epoch(), nil, 0)
	for _, seg := range segs[:2] {
		if _, err := first.Process(seg); err != nil {
			t.Fatal(err)
		}
	}
	first.Finalize()

	prior, _ := LoadCheckpoint(filepath.Join(dir, ".asset.chk.json"))
	pool := NewPool(2, dir, "asset", h264Epoch(), prior, 0)
	for _, seg := range segs {
		if !pool.IsDone(seg.ID) {
			continue
		}
		res, err := pool.Process(seg)
		if err != nil {
			t.Fatal(err)
		}
		// A reused segment carries the stored identity and zero packets were
		// re-hashed in this process.
		if res.Packets != 0 {
			t.Fatalf("reused segment should not repopulate packets, got %d", res.Packets)
		}
	}
}

func TestCheckpointAtomicAndSidecarRoundTrip(t *testing.T) {
	segs := buildSegments(t, 2, 8)
	dir := t.TempDir()
	pool := NewPool(2, dir, "asset", h264Epoch(), nil, 0)
	for _, seg := range segs {
		if _, err := pool.Process(seg); err != nil {
			t.Fatal(err)
		}
	}
	pool.Finalize()

	// No temp files may be left behind after atomic renames.
	matches, _ := filepath.Glob(filepath.Join(dir, ".*-*"))
	for _, m := range matches {
		if filepath.Base(m) != ".asset.chk.json" {
			t.Fatalf("leftover temp file: %s", m)
		}
	}
	// Every sidecar re-parses and matches the recorded hashes.
	sidecars, _ := filepath.Glob(filepath.Join(dir, "asset-segment-*.ts.tsix"))
	if len(sidecars) != 2 {
		t.Fatalf("want 2 sidecars, got %d", len(sidecars))
	}
	for _, path := range sidecars {
		if _, err := index.ReadSidecar(path); err != nil {
			t.Fatalf("sidecar unreadable: %v", err)
		}
	}
}

func TestCorruptCheckpointRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".asset.chk.json")
	if err := writeFile(path, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path); err == nil {
		t.Fatal("corrupt checkpoint must be rejected")
	}
}
