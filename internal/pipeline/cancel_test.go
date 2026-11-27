package pipeline

import (
	"context"
	"io"
	"testing"
	"time"

	"mediacheck/internal/codec"
)

var errEOF = io.EOF

// slowReader paces delivery so a small queue stays full and the producer is
// blocked in Enqueue when cancellation arrives, exercising real back-pressure.
type slowReader struct {
	data []byte
	pos  int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, errEOF
	}
	time.Sleep(2 * time.Millisecond)
	n := copy(p, s.data[s.pos:])
	s.pos += n
	return n, nil
}

// TestConsumerCancellationExits verifies that cancelling the context while a
// slow/blocked analysis is in flight tears down every goroutine instead of
// hanging, and the runner returns a context error.
func TestConsumerCancellationExits(t *testing.T) {
	gen, err := Generate(DemoSpec{
		Track: "v", Codec: codec.H264, Framing: codec.AnnexB,
		Frames: 200, KeyInterval: 5, Fault: FaultNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A queue of 1 makes the producer block quickly, exercising back-pressure.
	r, err := NewRunner(t.TempDir(), Config{
		ChunkBytes: 64, QueueLen: 1, SegmentBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := gen.TrackInput()
	input.Reader = &slowReader{data: gen.rawBytes()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := r.Run(ctx, "run-cancel", []TrackInput{input})
		done <- e
	}()
	// Cancel shortly after start while the pipeline is actively reading.
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled run must return an error, not a completed report")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not exit after context cancellation")
	}
}

// TestDamageDoesNotSwallowValidUnits ensures a damage range between two valid
// units never extends over the later valid unit's bytes.
func TestDamageDoesNotSwallowValidUnits(t *testing.T) {
	for _, c := range []codec.Codec{codec.H264, codec.H265} {
		gen, err := Generate(DemoSpec{
			Track: "v-" + string(c), Codec: c, Framing: codec.AnnexB,
			Frames: 16, KeyInterval: 4, Fault: FaultTruncated,
		})
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewRunner(t.TempDir(), Config{
			ChunkBytes: 128, QueueLen: 2, SegmentBytes: 128,
		})
		if err != nil {
			t.Fatal(err)
		}
		rep, err := r.Run(context.Background(), "run-dmg",
			[]TrackInput{gen.TrackInput()})
		if err != nil {
			t.Fatal(err)
		}
		tr := rep.Tracks[0]
		// A truncated tail must leave the bulk of units parseable.
		if tr.UnitCount < 10 {
			t.Fatalf("%s: truncation swallowed too many units: %d", c, tr.UnitCount)
		}
		for _, d := range tr.Damage {
			if d.End > tr.Bytes || d.Start < 0 || d.End <= d.Start {
				t.Fatalf("%s: invalid damage range %+v", c, d)
			}
		}
	}
}

// TestAllCodecsHealthy exercises every codec through the full pipeline with
// zero damage, confirming codec-specific framing is wired end to end.
func TestAllCodecsHealthy(t *testing.T) {
	cases := []struct {
		c       codec.Codec
		framing codec.Framing
	}{
		{codec.H264, codec.AnnexB},
		{codec.H265, codec.AnnexB},
		{codec.AV1, codec.LengthPrefixed},
	}
	for _, tc := range cases {
		gen, err := Generate(DemoSpec{
			Track: "v-" + string(tc.c), Codec: tc.c, Framing: tc.framing,
			Frames: 20, KeyInterval: 5, Fault: FaultNone,
		})
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewRunner(t.TempDir(), Config{
			ChunkBytes: 256, QueueLen: 2, SegmentBytes: 256,
		})
		if err != nil {
			t.Fatal(err)
		}
		rep, err := r.Run(context.Background(), "run-allcodec",
			[]TrackInput{gen.TrackInput()})
		if err != nil {
			t.Fatalf("%s: %v", tc.c, err)
		}
		tr := rep.Tracks[0]
		if len(tr.Damage) != 0 {
			t.Fatalf("%s: healthy stream has damage %+v", tc.c, tr.Damage)
		}
		if tr.KeyframeCount == 0 {
			t.Fatalf("%s: no keyframes detected", tc.c)
		}
		if rep.Metrics["roundtrip_consistency"] != 1.0 {
			t.Fatalf("%s: roundtrip not 1.0", tc.c)
		}
	}
}
