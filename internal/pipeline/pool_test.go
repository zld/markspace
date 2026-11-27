package pipeline

import (
	"testing"

	"continuity/internal/journal"
	"continuity/internal/model"
	"continuity/internal/transform"
)

func baseFrame(num model.FrameNumber, seq uint32, nanos int64) model.Frame {
	payload := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	return model.Frame{
		Number: num, Track: 1, Sequence: seq, CaptureNanos: nanos,
		SampleRate: 48000, Channels: model.LayoutMono, TargetLUFS: -23,
		PayloadLength: uint32(len(payload)), SHA256: model.Checksum(payload), Payload: payload,
	}
}

func tp() transform.Params {
	return transform.Params{
		SourceRate: 48000, TargetRate: 48000,
		Source: model.LayoutMono, Target: model.LayoutMono, TargetLUFS: -23,
	}
}

func TestRunSortsByTimeThenTrack(t *testing.T) {
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := New(4, j)
	if err != nil {
		t.Fatal(err)
	}
	// Hand jobs in shuffled order across two tracks and equal timestamps.
	jobs := []Job{
		{Frame: baseFrame(3, 3, 40), Params: tp()},
		{Frame: baseFrame(1, 1, 20), Params: tp()},
	}
	track2 := baseFrame(2, 2, 20)
	track2.Track = 2
	jobs = append(jobs, Job{Frame: track2, Params: tp()})

	out, err := pool.Run(jobs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(out))
	}
	// Frame 1 (t=20,track1), frame 2 (t=20,track2), frame 3 (t=40).
	wantOrder := []model.FrameNumber{1, 2, 3}
	for i, w := range wantOrder {
		if out[i].Frame.Number != w {
			t.Fatalf("position %d = frame %d, want %d", i, out[i].Frame.Number, w)
		}
	}
}

func TestRunRejectsDuplicateSequence(t *testing.T) {
	j, _ := journal.Open(t.TempDir())
	pool, _ := New(2, j)
	jobs := []Job{
		{Frame: baseFrame(1, 5, 20), Params: tp()},
		{Frame: baseFrame(2, 5, 40), Params: tp()},
	}
	_, err := pool.Run(jobs)
	se, ok := model.AsStructured(err)
	if !ok || se.Stage != model.StagePipeline {
		t.Fatalf("err = %v, want pipeline structured error", err)
	}
	if j.StateOf(2) != journal.StateFailed {
		t.Fatal("duplicate frame not marked failed")
	}
}

func TestRunRejectsZeroLengthAndCorruptFrame(t *testing.T) {
	cases := map[string]func(model.Frame) model.Frame{
		"zero-length": func(f model.Frame) model.Frame { f.Payload = nil; f.PayloadLength = 0; return f },
		"corrupt":     func(f model.Frame) model.Frame { f.Payload = []byte{9, 9, 9}; return f },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			j, _ := journal.Open(t.TempDir())
			pool, _ := New(2, j)
			f := mutate(baseFrame(1, 1, 20))
			_, err := pool.Run([]Job{{Frame: f, Params: tp()}})
			if err == nil {
				t.Fatalf("%s accepted", name)
			}
			if j.StateOf(1) != journal.StateFailed {
				t.Fatalf("%s did not mark frame failed", name)
			}
		})
	}
}

func TestRunRejectsTimeOverflow(t *testing.T) {
	j, _ := journal.Open(t.TempDir())
	pool, _ := New(2, j)
	f := baseFrame(1, 1, 20)
	// A capture time whose 90 kHz tick count cannot fit a uint32 RTP timestamp.
	f.CaptureNanos = 1 << 55
	_, err := pool.Run([]Job{{Frame: f, Params: tp()}})
	se, ok := model.AsStructured(err)
	if !ok || se.Stage != model.StagePipeline {
		t.Fatalf("err = %v, want pipeline overflow error", err)
	}
}
