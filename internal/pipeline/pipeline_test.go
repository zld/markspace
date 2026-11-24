package pipeline_test

import (
	"errors"
	"testing"

	"mediarun/internal/checkpoint"
	"mediarun/internal/pipeline"
	"mediarun/internal/testkit"
)

type fixture struct {
	dir    string
	params pipeline.Params
	in     *testkit.Inputs
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir()}
	f.params = pipeline.Params{
		Seed: 7, Rate: 48000, Layout: "stereo", LUFS: -16,
		Loss: 0.02, RTPWindow: 8, QueueCap: 4,
	}
	in, err := testkit.Generate(f.dir, f.params.Seed, f.params.Loss)
	if err != nil {
		t.Fatal(err)
	}
	f.in = in
	return f
}

func (f *fixture) runner(interruptAfter string) *pipeline.Runner {
	return &pipeline.Runner{
		Params: f.params, WorkDir: f.dir,
		MediaPath: f.in.MediaPath, RTPPath: f.in.RTPPath,
		MediaSHA: f.in.MediaSHA256, RTPSHA: f.in.RTPSHA256,
		InterruptAfter: interruptAfter,
	}
}

func TestInterruptAndResume(t *testing.T) {
	f := setup(t)
	if _, err := f.runner("audio").Run(); !errors.Is(err, pipeline.ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	res, err := f.runner("").Run() // 续跑
	if err != nil {
		t.Fatal(err)
	}
	if res.AVSyncMS > 20 || res.PacketRecoveryRate < 0.95 ||
		res.LatencyP95MS > 100 || res.MemoryPeakBytes > 67108864 {
		t.Fatalf("metrics out of bounds: %+v", res)
	}
	if res.Unrecovered != 12 {
		t.Fatalf("unrecovered=%d, want 12", res.Unrecovered)
	}
	if res.ErrorCount == 0 {
		t.Fatal("expected recoverable errors in manifest")
	}
	res2, err := f.runner("").Run() // 重试：相同输出摘要
	if err != nil {
		t.Fatal(err)
	}
	if res2.ResultSHA256 != res.ResultSHA256 {
		t.Fatalf("retry sha %s != %s", res2.ResultSHA256, res.ResultSHA256)
	}
}

func TestParamMismatchRejected(t *testing.T) {
	f := setup(t)
	f.runner("audio").Run() // 中断于 audio
	f.params.Rate = 44100   // 参数改变
	if _, err := f.runner("").Run(); !errors.Is(err, checkpoint.ErrMismatch) {
		t.Fatalf("want ErrMismatch, got %v", err)
	}
}

func TestTruncationInManifest(t *testing.T) {
	f := setup(t)
	res, err := f.runner("").Run()
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCount < 3 {
		t.Fatalf("want corrupt+missing+truncated errors, got %d", res.ErrorCount)
	}
}
