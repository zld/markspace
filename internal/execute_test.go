package medpipe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeCaptureFile persists a capture builder to a temp file and returns its
// path plus the scanned index.
func writeCaptureFile(t *testing.T, dir, track string, cb *captureBuilder, codec Codec) (string, TrackIndex) {
	t.Helper()
	path := filepath.Join(dir, track+".rtp")
	if err := os.WriteFile(path, cb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, errs := NewCaptureScanner(mustOpen(t, path), ScanOptions{
		Track: track, Codec: codec, Kind: KindVideo, ClockRate: 90000, ExpectedDelta: 3000,
	})
	if len(errs) > 0 {
		t.Fatalf("magic error: %v", errs)
	}
	scan, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	idx := BuildIndex(track, codec, KindVideo, 90000, digest, scan)
	return path, idx
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func buildMixedCapture(frames int) *captureBuilder {
	cb := newCapture()
	var seq uint16
	for i := 0; i < frames; i++ {
		var payload []byte
		if i%4 == 0 {
			payload = h264KeyFrame() // keyframe every fourth frame
		} else {
			payload = h264PFrame()
		}
		// Quality metadata interleaved as a separate packet on keyframes.
		if i%4 == 0 {
			meta := append([]byte{h264NALSEI}, buildMetaPayload(FrameMeta{Quality: 70 + uint8(i%10)})...)
			cb.Packet(seq, uint32(i*3000), true, meta)
			seq++
		}
		cb.Packet(seq, uint32(i*3000), true, payload)
		seq++
	}
	return cb
}

func TestExecutorChunksAndStats(t *testing.T) {
	dir := t.TempDir()
	cb := buildMixedCapture(10)
	path, idx := writeCaptureFile(t, dir, "v", cb, CodecH264)
	if idx.FrameCount != 10 || idx.KeyframeCount != 3 {
		t.Fatalf("index frames=%d keys=%d", idx.FrameCount, idx.KeyframeCount)
	}
	var seen []int
	e, err := NewExecutor(dir, path, &idx, ExecuteOptions{Track: "v", ChunkSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	err = e.Run(func(chunk []Frame) (int64, error) {
		var n int64
		for _, f := range chunk {
			seen = append(seen, f.Ordinal)
			n += int64(len(f.RawPayload))
		}
		return n, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 10 {
		t.Fatalf("visited %d frames, want 10", len(seen))
	}
	st := e.Stats()
	if st.FrameCount != 10 || st.KeyCount != 3 || st.Bytes <= 0 {
		t.Fatalf("stats wrong: %+v", st)
	}
	if st.QualityMin < 70 || st.QualityMean == 0 {
		t.Fatalf("quality aggregation wrong: %+v", st)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	// Successful completion clears the checkpoint.
	if cp, err := LoadCheckpoint(dir, "v"); err != nil || cp != nil {
		t.Fatalf("checkpoint not cleared: %v %v", cp, err)
	}
}

func TestExecutorResumeFromInterruption(t *testing.T) {
	dir := t.TempDir()
	cb := buildMixedCapture(10)
	path, idx := writeCaptureFile(t, dir, "v", cb, CodecH264)

	// First run: fail during the third chunk.
	e1, err := NewExecutor(dir, path, &idx, ExecuteOptions{Track: "v", ChunkSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("simulated I/O failure")
	var firstRun []int
	chunks := 0
	err = e1.Run(func(chunk []Frame) (int64, error) {
		chunks++
		for _, f := range chunk {
			firstRun = append(firstRun, f.Ordinal)
		}
		if chunks == 3 { // fail during the third chunk, before it commits
			return 0, injected
		}
		return int64(len(chunk)), nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("want injected error, got %v", err)
	}
	_ = e1.Close()

	cp, err := LoadCheckpoint(dir, "v")
	if err != nil || cp == nil || cp.NextOrdinal != 6 {
		t.Fatalf("checkpoint after interruption wrong: %+v err=%v", cp, err)
	}

	// Resume run: must restart at the keyframe at or before ordinal 5,
	// which is ordinal 4 (keyframes at 0, 4, 8).
	e2, err := NewExecutor(dir, path, &idx, ExecuteOptions{Track: "v", ChunkSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	var secondRun []int
	if err := e2.Run(func(chunk []Frame) (int64, error) {
		for _, f := range chunk {
			secondRun = append(secondRun, f.Ordinal)
		}
		return int64(len(chunk)), nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(secondRun) == 0 || secondRun[0] != 4 {
		t.Fatalf("resume must start at keyframe ordinal 4, got %v", secondRun)
	}
	if secondRun[len(secondRun)-1] != 9 {
		t.Fatalf("resume run should finish at ordinal 9, got %v", secondRun)
	}
	_ = e2.Close()
}

func TestExecutorRepeatable(t *testing.T) {
	dir := t.TempDir()
	cb := buildMixedCapture(10)
	path, idx := writeCaptureFile(t, dir, "v", cb, CodecH264)

	run := func() ExecStats {
		e, err := NewExecutor(dir, path, &idx, ExecuteOptions{Track: "v", ChunkSize: 4})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Run(func(chunk []Frame) (int64, error) {
			var n int64
			for _, f := range chunk {
				n += int64(len(f.RawPayload))
			}
			return n, nil
		}); err != nil {
			t.Fatal(err)
		}
		st := e.Stats()
		_ = e.Close()
		return st
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("repeated runs produced different stats:\n%+v\n%+v", a, b)
	}
}

func TestExecutorEmptyTrack(t *testing.T) {
	dir := t.TempDir()
	cb := newCapture() // magic only, no records
	path := filepath.Join(dir, "empty.rtp")
	if err := os.WriteFile(path, cb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, _ := HashFile(path)
	s, _ := NewCaptureScanner(mustOpen(t, path), ScanOptions{
		Track: "empty", Codec: CodecH264, Kind: KindVideo, ClockRate: 90000,
	})
	scan, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	idx := BuildIndex("empty", CodecH264, KindVideo, 90000, digest, scan)
	e, err := NewExecutor(dir, path, &idx, ExecuteOptions{Track: "empty", ChunkSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(func(chunk []Frame) (int64, error) {
		t.Fatal("empty track must not invoke visit")
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	if st := e.Stats(); st.FrameCount != 0 {
		t.Fatalf("empty track stats wrong: %+v", st)
	}
	_ = e.Close()
}

func TestExecutorBoundaryChunkSizes(t *testing.T) {
	dir := t.TempDir()
	cb := buildMixedCapture(8)
	path, idx := writeCaptureFile(t, dir, "v", cb, CodecH264)
	// Chunk size larger than the whole track exercises the final partial
	// chunk path in a single iteration.
	e, err := NewExecutor(dir, path, &idx, ExecuteOptions{Track: "v", ChunkSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := e.Run(func(chunk []Frame) (int64, error) {
		count += len(chunk)
		return int64(len(chunk)), nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("want 8 frames in one boundary chunk, got %d", count)
	}
	_ = e.Close()
}
