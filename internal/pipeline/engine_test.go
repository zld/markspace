package pipeline

import (
	"context"
	"testing"
	"time"

	"mediad/internal/recover"
	"mediad/internal/wav"
)

func testConfig(dir string) Config {
	return Config{
		OutputDir:        dir,
		Chunks:           6,
		FramesPerChunk:   32,
		QueueCapacity:    2,
		CheckpointEvery:  3,
		Channels:         2,
		SampleRate:       48000,
		BitsPerSample:    16,
		PacketsPerChunk:  4,
		MaxReorder:       8,
		SlowConsumerDelay: time.Millisecond,
	}
}

// buildWAVChunks creates one header-bearing first chunk and raw PCM chunks.
func buildWAVChunks(t *testing.T, cfg Config) [][]byte {
	t.Helper()
	format := wav.Format{
		AudioFormat: 1, NumChannels: cfg.Channels,
		SampleRate: cfg.SampleRate, BitsPerSample: cfg.BitsPerSample,
	}
	frameSize := format.FrameSize()
	totalFrames := cfg.Chunks * cfg.FramesPerChunk
	pcm := make([]byte, totalFrames*frameSize)
	for i := range pcm {
		pcm[i] = byte(i*13 + 7)
	}
	whole, err := wav.Encode(format, pcm)
	if err != nil {
		t.Fatal(err)
	}
	headerLen := len(whole) - len(pcm)
	chunks := make([][]byte, cfg.Chunks)
	for c := 0; c < cfg.Chunks; c++ {
		start := headerLen + c*cfg.FramesPerChunk*frameSize
		end := start + cfg.FramesPerChunk*frameSize
		if c == 0 {
			chunks[c] = append(append([]byte(nil), whole[:headerLen]...), whole[start:end]...)
		} else {
			chunks[c] = whole[start:end]
		}
	}
	return chunks
}

func runEngine(t *testing.T, eng *Engine, chunks [][]byte, plan *recover.Plan) *Metrics {
	t.Helper()
	ctx := context.Background()
	feedErr := make(chan error, 1)
	go func() {
		for c, data := range chunks {
			if c < plan.ResumeAtChunk {
				if err := eng.FeedSkippedWAV(data); err != nil {
					feedErr <- err
					return
				}
				continue
			}
			if err := eng.FeedWAV(ctx, c, data); err != nil {
				feedErr <- err
				return
			}
		}
		if err := eng.FinalizeWAV(); err != nil {
			feedErr <- err
			return
		}
		eng.CloseInput()
		feedErr <- nil
	}()
	m, err := eng.Process(ctx)
	if err := <-feedErr; err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEndToEndPipelineVerifiesAllSegments(t *testing.T) {
	cfg := testConfig(t.TempDir())
	chunks := buildWAVChunks(t, cfg)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := eng.Resume()
	if err != nil {
		t.Fatal(err)
	}
	m := runEngine(t, eng, chunks, plan)
	if m.Segments != cfg.Chunks {
		t.Fatalf("segments=%d want %d", m.Segments, cfg.Chunks)
	}
	if m.RoundTripOK != cfg.Chunks || m.HashMatches != cfg.Chunks || m.KeyframeHits != cfg.Chunks {
		t.Fatalf("verification counters incomplete: %+v", m)
	}
	if m.PeakQueueChunks > cfg.QueueCapacity {
		t.Fatalf("peak queue %d exceeded capacity %d", m.PeakQueueChunks, cfg.QueueCapacity)
	}
}

func TestResumeSkipsAlreadyCommittedSegments(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	chunks := buildWAVChunks(t, cfg)

	first, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := first.Resume()
	if err != nil {
		t.Fatal(err)
	}
	if m := runEngine(t, first, chunks, plan); m.Segments != cfg.Chunks {
		t.Fatalf("first run segments=%d", m.Segments)
	}

	// A fresh engine over the same directory must resume and skip everything.
	second, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	replan, err := second.Resume()
	if err != nil {
		t.Fatal(err)
	}
	if replan.LastGoodSegment != cfg.Chunks-1 || replan.ResumeAtChunk != cfg.Chunks {
		t.Fatalf("resume plan wrong: %+v", replan)
	}
	m := runEngine(t, second, chunks, replan)
	if m.Segments != 0 {
		t.Fatalf("verified segments were rebuilt: %d", m.Segments)
	}
}

func TestCrashRecoveryRedoesOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	chunks := buildWAVChunks(t, cfg)

	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := eng.Resume()
	if m := runEngine(t, eng, chunks, plan); m.Segments != cfg.Chunks {
		t.Fatalf("initial run segments=%d", m.Segments)
	}
	if err := eng.SimulateCrash(); err != nil {
		t.Fatal(err)
	}
	eng.ResetForRerun()
	replan, redo, err := eng.RerunAfterCrash()
	if err != nil {
		t.Fatal(err)
	}
	if redo <= 0 || replan.ResumeAtChunk >= cfg.Chunks {
		t.Fatalf("recovery plan expected tail redo, got %+v redo=%d", replan, redo)
	}
	m := runEngine(t, eng, chunks, replan)
	if m.Segments != redo {
		t.Fatalf("redone segments=%d want %d", m.Segments, redo)
	}
	if m.RoundTripOK != redo || m.HashMatches != redo {
		t.Fatalf("redone work not fully verified: %+v", m)
	}
}
