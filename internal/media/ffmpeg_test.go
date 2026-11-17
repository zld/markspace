package media

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFFmpegRoundTrip(t *testing.T) {
	if err := EnsureFFmpeg(context.Background()); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.y4m")
	if _, err := GenerateDemo(DemoConfig{Path: src, Width: 96, Height: 64,
		FPSNum: 25, FPSDen: 1, Segments: 1, FramesPerSegment: 24}); err != nil {
		t.Fatal(err)
	}
	m4v := filepath.Join(dir, "out.m4v")
	size, err := Encode(context.Background(), EncodeOptions{InputY4M: src, OutputM4V: m4v, QP: 10})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if size <= 0 {
		t.Fatal("non-positive encoded size")
	}
	dec := filepath.Join(dir, "dec.y4m")
	if err := Decode(context.Background(), m4v, dec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	st, err := CompareY4M(src, dec)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if st.Frames != 24 {
		t.Errorf("want 24 compared frames, got %d", st.Frames)
	}
	if st.PSNRMean <= 0 || st.PSNRMean > PerfectPSNR+0.01 {
		t.Errorf("implausible mean PSNR %f", st.PSNRMean)
	}
	if st.PSNRMin > st.PSNRMean || st.PSNRMax < st.PSNRMean {
		t.Errorf("min/mean/max ordering wrong: %+v", st)
	}
}

func TestPSNRBitrateTradeoff(t *testing.T) {
	if err := EnsureFFmpeg(context.Background()); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.y4m")
	if _, err := GenerateDemo(DemoConfig{Path: src, Width: 96, Height: 64,
		FPSNum: 25, FPSDen: 1, Segments: 2, FramesPerSegment: 30}); err != nil {
		t.Fatal(err)
	}
	run := func(qp int) (int64, float64) {
		m4v := filepath.Join(dir, "q.m4v")
		size, err := Encode(context.Background(), EncodeOptions{src, m4v, qp})
		if err != nil {
			t.Fatalf("encode qp=%d: %v", qp, err)
		}
		dec := filepath.Join(dir, "q.y4m")
		if err := Decode(context.Background(), m4v, dec); err != nil {
			t.Fatalf("decode qp=%d: %v", qp, err)
		}
		st, err := CompareY4M(src, dec)
		if err != nil {
			t.Fatalf("psnr qp=%d: %v", qp, err)
		}
		return size, st.PSNRMean
	}
	loSize, loPSNR := run(3)
	hiSize, hiPSNR := run(30)
	if loSize <= hiSize {
		t.Errorf("expected QP3 size %d > QP30 size %d", loSize, hiSize)
	}
	if loPSNR <= hiPSNR {
		t.Errorf("expected QP3 PSNR %f > QP30 PSNR %f", loPSNR, hiPSNR)
	}
}

func TestEncodeRejectsBadQP(t *testing.T) {
	if _, err := Encode(context.Background(), EncodeOptions{"/tmp/x", "/tmp/y", 1}); err == nil {
		t.Fatal("QP 1 should be rejected")
	}
}
