package pipeline_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"archivecheck/internal/fault"
	"archivecheck/internal/pipeline"
)

// sumStage records the digest of everything it sees and can be slowed down.
type sumStage struct {
	mu   sync.Mutex
	hash interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	delay time.Duration
	seen  int64
}

func newSumStage(delay time.Duration) *sumStage {
	return &sumStage{hash: sha256.New(), delay: delay}
}

func (s *sumStage) stage() pipeline.Stage {
	return pipeline.Stage{Name: "digest", Handle: func(ctx context.Context, c pipeline.Chunk) error {
		if s.delay > 0 {
			select {
			case <-time.After(s.delay):
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		s.mu.Lock()
		s.hash.Write(c.Data)
		s.seen += int64(len(c.Data))
		s.mu.Unlock()
		return nil
	}}
}

func (s *sumStage) sum() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return hex.EncodeToString(s.hash.Sum(nil))
}

func TestRunStreamsEveryByteInOrder(t *testing.T) {
	data := bytes.Repeat([]byte("archive-pipeline-"), 5000) // 85 000 bytes
	var order []int64
	var mu sync.Mutex
	collect := pipeline.Stage{Name: "collect", Handle: func(ctx context.Context, c pipeline.Chunk) error {
		if c.Offset != int64(c.Index*4096) {
			t.Errorf("chunk %d offset = %d", c.Index, c.Offset)
		}
		mu.Lock()
		order = append(order, c.Offset)
		mu.Unlock()
		return nil
	}}
	last := newSumStage(0)
	rep, err := pipeline.Run(context.Background(), bytes.NewReader(data), int64(len(data)),
		pipeline.Config{ChunkSize: 4096, QueueSize: 2}, collect, last.stage())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Stages[1].Bytes != int64(len(data)) {
		t.Errorf("stage bytes = %d, want %d", rep.Stages[1].Bytes, len(data))
	}
	want := sha256.Sum256(data)
	if last.sum() != hex.EncodeToString(want[:]) {
		t.Error("stage digest does not match the input")
	}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("chunks out of order at %d", i)
		}
	}
}

func TestRunBoundsOutstandingChunks(t *testing.T) {
	// A deliberately slow consumer with a large input: if the pipeline buffered
	// without bound the live-chunk peak would grow with the input.
	const size = 2 << 20
	data := make([]byte, size)
	slow := pipeline.Stage{Name: "slow", Handle: func(ctx context.Context, c pipeline.Chunk) error {
		time.Sleep(time.Millisecond)
		return nil
	}}
	// Two stages so there is an internal queue that can fill and apply
	// backpressure while the slow stage holds a chunk.
	fast := pipeline.Stage{Name: "fast", Handle: func(ctx context.Context, c pipeline.Chunk) error { return nil }}
	cfg := pipeline.Config{ChunkSize: 1024, QueueSize: 2}
	rep, err := pipeline.Run(context.Background(), bytes.NewReader(data), size, cfg, fast, slow)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PeakOutstanding > rep.OutstandingCeiling {
		t.Errorf("peak outstanding %d exceeded the configured bound %d", rep.PeakOutstanding, rep.OutstandingCeiling)
	}
	if rep.BackpressureWaits == 0 {
		t.Error("a slow consumer should have caused the producer to wait at least once")
	}
	if rep.Stages[0].Waits == 0 {
		t.Error("stage 0 should record backpressure waits when stage 1 is slow")
	}
}

func TestRunMemoryDoesNotGrowWithInput(t *testing.T) {
	if testing.Short() {
		t.Skip("memory measurement is slow")
	}
	// Track the live chunk count reported by the run itself at a fixed
	// configuration: it must not grow with the input length.
	measure := func(size int64) int {
		stage := pipeline.Stage{Name: "sink", Handle: func(ctx context.Context, c pipeline.Chunk) error { return nil }}
		rep, err := pipeline.Run(context.Background(), io.LimitReader(zeroReader{}, size), size,
			pipeline.Config{ChunkSize: 4096, QueueSize: 1}, stage)
		if err != nil {
			t.Fatal(err)
		}
		return rep.PeakOutstanding
	}
	small := measure(256 << 10)
	large := measure(8 << 20)
	if large > small {
		t.Errorf("live chunks grew with input: small=%d large=%d", small, large)
	}
	if large > (2)*(1+1) {
		t.Errorf("peak outstanding %d exceeds the two-stage bound", large)
	}
}

// zeroReader yields zero bytes forever.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// repeatingReader yields fill over and over.
type repeatingReader struct{ fill []byte }

func (r repeatingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return copy(p, r.fill), nil
}

func TestCancelSharesOneCause(t *testing.T) {
	cause := fault.New(fault.StagePipeline, fault.KindCancelled, "demo.bin", 8192, "cancelled by caller")
	var reached int
	blocker := pipeline.Stage{Name: "blocker", Handle: func(ctx context.Context, c pipeline.Chunk) error {
		reached++
		if c.Offset >= 4096 {
			return cause
		}
		return nil
	}}
	tail := newSumStage(0)
	rep, err := pipeline.Run(context.Background(), bytes.NewReader(make([]byte, 1<<20)), 1<<20,
		pipeline.Config{ChunkSize: 4096, QueueSize: 2}, blocker, tail.stage())
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if !errors.Is(err, cause) {
		t.Errorf("run error = %v, want the stage's cause", err)
	}
	for _, st := range rep.Stages {
		if st.Err == nil || !errors.Is(st.Err, cause) {
			t.Errorf("stage %s reported %v, want the shared cause", st.Name, st.Err)
		}
	}
}

func TestContextCancelBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := pipeline.Run(ctx, bytes.NewReader([]byte("abc")), 3,
		pipeline.DefaultConfig(), pipeline.Stage{Name: "noop", Handle: func(context.Context, pipeline.Chunk) error { return nil }})
	if err == nil {
		t.Fatal("cancelled context should fail the run")
	}
	if rep.PeakOutstanding > rep.OutstandingCeiling {
		t.Errorf("peak %d above ceiling %d", rep.PeakOutstanding, rep.OutstandingCeiling)
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []pipeline.Config{
		{ChunkSize: 0, QueueSize: 1},
		{ChunkSize: 1, QueueSize: 0},
		{ChunkSize: pipeline.MaxChunkSize + 1, QueueSize: 1},
		{ChunkSize: 1, QueueSize: pipeline.MaxQueueSize + 1},
	}
	for _, cfg := range cases {
		_, err := pipeline.Run(context.Background(), bytes.NewReader(nil), 0, cfg,
			pipeline.Stage{Name: "noop", Handle: func(context.Context, pipeline.Chunk) error { return nil }})
		if !fault.HasKind(err, fault.KindInvalid) && !fault.HasKind(err, fault.KindLimit) {
			t.Errorf("%+v: expected a configuration error, got %v", cfg, err)
		}
	}
	if got := pipeline.DefaultConfig().Bound(2); got != 9 {
		t.Errorf("bound = %d, want 9", got)
	}
	if _, err := pipeline.Run(context.Background(), bytes.NewReader(nil), 0, pipeline.DefaultConfig()); err == nil {
		t.Error("a pipeline with no stages should fail")
	}
}
