package index

import (
	"os"
	"path/filepath"
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/mkv"
	"tracecheck/internal/serr"
)

func sampleBlocks() []mkv.Block {
	mk := func(track uint64, ticks int64, key bool, idx int, start int64) mkv.Block {
		return mkv.Block{
			TrackNumber:  track,
			Index:        idx,
			Keyframe:     key,
			PayloadStart: start,
			PayloadEnd:   start + 100,
			Cluster: mkv.ClusterRef{
				Index: idx / 2, Offset: start - 20, End: start + 200,
				Timestamp: ticks,
			},
		}
	}
	return []mkv.Block{
		mk(1, 0, true, 0, 1000),
		mk(2, 0, true, 1, 1100),
		mk(1, 40, false, 2, 1200),
		mk(1, 80, true, 3, 1300),
		mk(2, 80, true, 4, 1400),
	}
}

func testTimebase() clock.Timebase {
	return clock.Timebase{TickNanos: clock.NanosPerMilli, ClientUnitNanos: clock.NanosPerMilli}
}

func TestBuildOrderAndKeyframes(t *testing.T) {
	ix, err := Build(sampleBlocks(), testTimebase(), clock.NanosPerMilli, "1ms")
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(ix.Tracks))
	}
	v := ix.Track(1)
	if v == nil || len(v.Records) != 3 {
		t.Fatalf("track 1 records: %+v", v)
	}
	// Times must be non-decreasing.
	for i := 1; i < len(v.Records); i++ {
		if v.Records[i].TimeNanos < v.Records[i-1].TimeNanos {
			t.Fatalf("records out of time order: %+v", v.Records)
		}
	}
	if len(v.Keyframes) != 2 {
		t.Errorf("track 1 keyframes=%v", v.Keyframes)
	}
	if k := v.KeyframeBefore(60 * clock.NanosPerMilli); k < 0 ||
		v.Records[k].TimeNanos != 0 {
		t.Errorf("keyframe before 60ms = %d", k)
	}
	if k, ok := v.KeyframeAt(80 * clock.NanosPerMilli); !ok || !v.Records[k].Keyframe {
		t.Errorf("keyframe at 80ms not found")
	}
}

func TestBuildPreservesRawOffsets(t *testing.T) {
	blocks := sampleBlocks()
	ix, err := Build(blocks, testTimebase(), clock.NanosPerMilli, "1ms")
	if err != nil {
		t.Fatal(err)
	}
	byIndex := map[int]Record{}
	for _, ti := range ix.Tracks {
		for _, r := range ti.Records {
			byIndex[r.BlockIndex] = r
		}
	}
	for _, b := range blocks {
		r, ok := byIndex[b.Index]
		if !ok {
			t.Fatalf("block %d missing", b.Index)
		}
		if r.SampleStart != b.PayloadStart || r.SampleEnd != b.PayloadEnd ||
			r.ClusterStart != b.Cluster.Offset || r.ClusterEnd != b.Cluster.End {
			t.Errorf("offsets not preserved for block %d: %+v", b.Index, r)
		}
	}
}

func TestDigestStable(t *testing.T) {
	a, _ := Build(sampleBlocks(), testTimebase(), clock.NanosPerMilli, "1ms")
	b, _ := Build(sampleBlocks(), testTimebase(), clock.NanosPerMilli, "1ms")
	if d1, d2 := a.Digest(), b.Digest(); d1 != d2 {
		t.Fatalf("digest not stable: %s vs %s", d1, d2)
	}
	// Changing a byte in the block layout changes the digest.
	mutated := sampleBlocks()
	mutated[0].PayloadStart++
	c, _ := Build(mutated, testTimebase(), clock.NanosPerMilli, "1ms")
	if c.Digest() == a.Digest() {
		t.Fatal("digest should change when offsets change")
	}
}

func TestSpanAndErrors(t *testing.T) {
	ix, _ := Build(sampleBlocks(), testTimebase(), clock.NanosPerMilli, "1ms")
	lo, hi, err := ix.Span()
	if err != nil {
		t.Fatal(err)
	}
	if lo != 0 || hi != 80*clock.NanosPerMilli {
		t.Errorf("span=[%d,%d]", lo, hi)
	}
	empty, _ := Build(nil, testTimebase(), clock.NanosPerMilli, "1ms")
	if _, _, err := empty.Span(); serr.CodeOf(err) != serr.CodeRangeOutOfBounds {
		t.Fatalf("empty span error=%v", err)
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := CheckpointPath(dir, "sample.mkv")
	cp := &Checkpoint{
		InputPath:       "/input/sample.mkv",
		InputSHA256:     "abc123",
		InputSize:       1000,
		CompletedOffset: 600,
		CompletedChunks: 3,
		TotalChunks:     5,
		IndexDigest:     "deadbeefdeadbeef",
		MetadataDigest:  "cafebabecafebabe",
		SegmentHashes:   []string{"a", "b", "c"},
		ChunkSize:       200,
	}
	if err := SaveCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedOffset != 600 || got.CompletedChunks != 3 || len(got.SegmentHashes) != 3 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if err := got.VerifyAgainst("abc123", 1000, 200); err != nil {
		t.Fatalf("verify should pass: %v", err)
	}
	if err := got.VerifyAgainst("different", 1000, 200); serr.CodeOf(err) != serr.CodeCheckpointCorrupt {
		t.Fatalf("digest mismatch not caught: %v", err)
	}
}

func TestLoadCheckpointMissingIsNil(t *testing.T) {
	cp, err := LoadCheckpoint(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || cp != nil {
		t.Fatalf("missing checkpoint: cp=%v err=%v", cp, err)
	}
}

func TestLoadCheckpointCorrupt(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"invalid-json": `{not json`,
		"bad-version":  `{"version":99,"input_path":"x","input_sha256":"y","input_size":10,"completed_offset":0,"completed_chunks":0,"total_chunks":0,"chunk_size":1}`,
		"offset-past":  `{"version":1,"input_path":"x","input_sha256":"y","input_size":10,"completed_offset":11,"completed_chunks":0,"total_chunks":0,"chunk_size":1}`,
		"hash-count":   `{"version":1,"input_path":"x","input_sha256":"y","input_size":10,"completed_offset":0,"completed_chunks":2,"total_chunks":2,"chunk_size":1,"segment_hashes":["only"]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadCheckpoint(path)
			if serr.CodeOf(err) != serr.CodeCheckpointCorrupt {
				t.Fatalf("want CHECKPOINT_CORRUPT, got %v", err)
			}
		})
	}
}
