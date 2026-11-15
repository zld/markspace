package codec

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"transcode/internal/media"
)

// buildStub compiles the encoder double once for the package tests.
func buildStub(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stubencoder")
	if err := buildToBin(bin); err != nil {
		t.Fatalf("build stubencoder: %v", err)
	}
	return bin
}

func writeInput(t *testing.T, path string) {
	t.Helper()
	v := &media.Video{Width: 8, Height: 6, FPS: 10,
		Frames: make([][]byte, 20)}
	for i := range v.Frames {
		frame := make([]byte, 48)
		for p := range frame {
			frame[p] = byte((i*7 + p*3) % 256)
		}
		v.Frames[i] = frame
	}
	if err := media.WriteVideo(path, v); err != nil {
		t.Fatal(err)
	}
}

func TestRunSuccessAndBitrateAccuracy(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.vid")
	out := filepath.Join(dir, "seg.enc")
	writeInput(t, in)
	bin := buildStub(t)
	tmpl := bin + " --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}"
	r := NewRunner()
	res, err := r.Run(context.Background(), Spec{
		CommandTemplate: tmpl, InputPath: in, OutputPath: out,
		TargetKbps: 480, DurationSec: 2, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatalf("expected success, got %v (%s)", err, res.FailureNote)
	}
	if res.Bytes <= 0 || res.Attempts < 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if abs(res.Deviation) > DefaultTolerancePct {
		t.Fatalf("deviation %.2f%% exceeds tolerance", res.Deviation)
	}
}

func TestRunRejectsBadTemplate(t *testing.T) {
	r := NewRunner()
	for _, tmpl := range []string{"", "encoder --only {in}", "encoder {in} {out} {bogus}"} {
		if _, err := r.Run(context.Background(), Spec{
			CommandTemplate: tmpl, InputPath: "a", OutputPath: "b",
			TargetKbps: 100, DurationSec: 1}); err == nil {
			t.Fatalf("template %q should be rejected", tmpl)
		}
	}
}

func TestRunEncoderNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.vid")
	writeInput(t, in)
	bin := buildStub(t)
	tmpl := bin + " --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration} --fail 7"
	r := NewRunner()
	res, err := r.Run(context.Background(), Spec{
		CommandTemplate: tmpl, InputPath: in,
		OutputPath: filepath.Join(dir, "x.enc"),
		TargetKbps: 480, DurationSec: 2})
	if err == nil || res == nil || res.ExitCode != 7 {
		t.Fatalf("expected exit-7 failure record, got %v %+v", err, res)
	}
	if !strings.Contains(res.FailureNote, "exit 7") {
		t.Fatalf("failure note should mention exit code: %q", res.FailureNote)
	}
}

func TestRunTruncatedOutputFailsVerification(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.vid")
	out := filepath.Join(dir, "seg.enc")
	writeInput(t, in)
	bin := buildStub(t)
	tmpl := bin + " --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration} --truncate 40"
	r := NewRunner()
	_, err := r.Run(context.Background(), Spec{
		CommandTemplate: tmpl, InputPath: in, OutputPath: out,
		TargetKbps: 480, DurationSec: 2})
	if err == nil {
		t.Fatal("truncated output must fail verification on both attempts")
	}
}

func TestRunOversizeRejected(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.vid")
	out := filepath.Join(dir, "seg.enc")
	writeInput(t, in)
	bin := buildStub(t)
	tmpl := bin + " --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}"
	r := NewRunner()
	_, err := r.Run(context.Background(), Spec{
		CommandTemplate: tmpl, InputPath: in, OutputPath: out,
		TargetKbps: 480, DurationSec: 2, MaxOutputBytes: 10})
	if err == nil {
		t.Fatal("oversize output must be rejected")
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
