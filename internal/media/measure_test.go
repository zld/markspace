package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeasurePSNRRejectsGarbage(t *testing.T) {
	tool := requireFFmpeg(t)
	dir := t.TempDir()
	syn := Synthetic{Width: 32, Height: 32, FPS: 5, Seconds: 1, Seed: 1}
	ref, _, err := tool.Generate(dir, syn)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.h264")
	if err := os.WriteFile(bad, []byte("not a stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.MeasurePSNR(ref, bad, syn); err == nil {
		t.Fatal("expected failure for garbage input")
	}
}

func TestProbeMP4AndRemux(t *testing.T) {
	tool := requireFFmpeg(t)
	dir := t.TempDir()
	syn := Synthetic{Width: 64, Height: 64, FPS: 10, Seconds: 1, Seed: 5}
	ref, wav, err := tool.Generate(dir, syn)
	if err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(dir, "v.h264")
	if _, err := tool.EncodeTarget(ref, raw, "h264", syn, 200, "ultrafast"); err != nil {
		t.Fatal(err)
	}
	mp4 := filepath.Join(dir, "v.mp4")
	if err := tool.Remux(raw, wav, mp4, "h264", syn); err != nil {
		t.Fatal(err)
	}
	probe, err := tool.ProbeMP4(mp4)
	if err != nil {
		t.Fatal(err)
	}
	if probe.VideoSec < 0.9 || probe.VideoSec > 1.1 || probe.AudioSec < 0.9 || probe.AudioSec > 1.2 {
		t.Fatalf("probe = %+v", probe)
	}
	if probe.CodecName != "h264" {
		t.Fatalf("codec = %s", probe.CodecName)
	}
	digest, err := FileDigest(mp4)
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest = %q err=%v", digest, err)
	}
}

func TestMeasurePSNRAcceptsGood(t *testing.T) {
	tool := requireFFmpeg(t)
	dir := t.TempDir()
	syn := Synthetic{Width: 96, Height: 64, FPS: 10, Seconds: 1, Seed: 9}
	ref, _, err := tool.Generate(dir, syn)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "good.h264")
	if _, err := tool.EncodeTarget(ref, out, "h264", syn, 400, "ultrafast"); err != nil {
		t.Fatal(err)
	}
	psnr, err := tool.MeasurePSNR(ref, out, syn)
	if err != nil {
		t.Fatal(err)
	}
	if psnr < 20 {
		t.Fatalf("psnr = %v", psnr)
	}
}
