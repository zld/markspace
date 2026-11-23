package index

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/clock"
)

func entry(track, logical uint32, t int64) Entry {
	return Entry{
		Track: track, Logical: logical, Mfhd: logical + 1,
		Time: t, Duration: 3000, Timescale: 90000,
		Source: "track-0001/frag-000000.mp4", Keyframe: true,
		Hash: [32]byte{byte(logical)},
	}
}

func TestChunkRoundtrip(t *testing.T) {
	dir := t.TempDir()
	c := Chunk{Index: 0, Entries: []Entry{entry(1, 0, 0), entry(1, 1, 3000)}}
	if err := WriteChunk(dir, c); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	got, ok, err := LoadChunk(dir, 0)
	if err != nil || !ok {
		t.Fatalf("LoadChunk: ok=%v err=%v", ok, err)
	}
	if got.Checksum() != c.Checksum() || len(got.Entries) != 2 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if _, err := os.Stat(ChunkPath(dir, 0)); err != nil {
		t.Errorf("chunk file missing: %v", err)
	}
}

func TestLoadChunkRejectsIncomplete(t *testing.T) {
	dir := t.TempDir()
	c := Chunk{Index: 0, Entries: []Entry{entry(1, 0, 0)}}
	if err := WriteChunk(dir, c); err != nil {
		t.Fatal(err)
	}
	// Corrupt the chunk data: checksum no longer matches.
	if err := os.WriteFile(ChunkPath(dir, 0), []byte(`{"index":0,"entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadChunk(dir, 0); err != nil || ok {
		t.Errorf("corrupt chunk must be rejected: ok=%v err=%v", ok, err)
	}
	// Remove the done marker: incomplete.
	if err := WriteChunk(dir, c); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "chunk-000000.done")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := LoadChunk(dir, 0); ok {
		t.Error("chunk without done marker must be rejected")
	}
	// A half-written chunk (truncated JSON) is rejected.
	if err := os.WriteFile(ChunkPath(dir, 0), []byte(`{"index":0,"entries":[`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := LoadChunk(dir, 0); ok {
		t.Error("truncated chunk must be rejected")
	}
	// Missing chunk is simply absent.
	if _, ok, err := LoadChunk(dir, 7); err != nil || ok {
		t.Errorf("missing chunk: ok=%v err=%v", ok, err)
	}
}

func TestWriteChunkAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	if err := WriteChunk(dir, Chunk{Index: 0, Entries: []Entry{entry(1, 0, 0)}}); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(matches) != 0 {
		t.Errorf("temporary files left behind: %v", matches)
	}
}

func TestIndexMonotonicAndQueries(t *testing.T) {
	ix := New()
	for i, tm := range []int64{0, 3000, 6000} {
		if err := ix.Add(entry(1, uint32(i), tm)); err != nil {
			t.Fatal(err)
		}
	}
	// Non-monotonic entry is refused.
	if err := ix.Add(entry(1, 3, 1000)); !clock.HasCategory(err, clock.CatInvariant) {
		t.Errorf("want invariant error, got %v", err)
	}
	// Non-keyframe entry is refused.
	bad := entry(1, 3, 9000)
	bad.Keyframe = false
	if err := ix.Add(bad); !clock.HasCategory(err, clock.CatInvariant) {
		t.Errorf("want invariant error, got %v", err)
	}
	// Boundary queries.
	if _, ok := ix.Query(1, -1); ok {
		t.Error("query before first keyframe must miss")
	}
	if e, ok := ix.Query(1, 0); !ok || e.Time != 0 {
		t.Error("query at first keyframe must hit")
	}
	if e, ok := ix.Query(1, 5999); !ok || e.Time != 3000 {
		t.Errorf("query between keyframes: %+v", e)
	}
	if e, ok := ix.Query(1, 1<<40); !ok || e.Time != 6000 {
		t.Errorf("query past last keyframe returns last: %+v", e)
	}
	if _, ok := ix.Query(99, 1<<40); ok {
		t.Error("unknown track must miss")
	}
	if e, ok := ix.First(1); !ok || e.Time != 0 {
		t.Error("first")
	}
	if e, ok := ix.Last(1); !ok || e.Time != 6000 {
		t.Error("last")
	}
}

func TestQuerySecondsRounding(t *testing.T) {
	ix := New()
	// Keyframes at 0 and 1.5s on a 90000Hz track.
	if err := ix.Add(entry(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := ix.Add(entry(1, 1, 135000)); err != nil {
		t.Fatal(err)
	}
	// 1.4999...s must resolve to the first keyframe, never overshoot.
	sec := clock.NewRational(14999, 10000)
	e, ok := ix.QuerySeconds(1, sec, 90000)
	if !ok || e.Time != 0 {
		t.Errorf("rounding down must not overshoot: %+v", e)
	}
	// Exactly 1.5s hits the second keyframe.
	e, ok = ix.QuerySeconds(1, clock.NewRational(3, 2), 90000)
	if !ok || e.Time != 135000 {
		t.Errorf("exact hit: %+v", e)
	}
}

func TestConvertTicksRounding(t *testing.T) {
	// 1 tick at 48000Hz -> 90000Hz is 1.875: rounds to 2, 1, 2.
	if got := clock.ConvertTicks(1, 48000, 90000, clock.RoundNearest); got != 2 {
		t.Errorf("nearest: %d", got)
	}
	if got := clock.ConvertTicks(1, 48000, 90000, clock.RoundDown); got != 1 {
		t.Errorf("down: %d", got)
	}
	if got := clock.ConvertTicks(1, 48000, 90000, clock.RoundUp); got != 2 {
		t.Errorf("up: %d", got)
	}
	// Negative values round toward/away from infinity consistently.
	if got := clock.ConvertTicks(-1, 48000, 90000, clock.RoundDown); got != -2 {
		t.Errorf("negative down: %d", got)
	}
	if got := clock.ConvertTicks(-1, 48000, 90000, clock.RoundUp); got != -1 {
		t.Errorf("negative up: %d", got)
	}
}
