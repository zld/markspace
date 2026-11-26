package task

import (
	"audionorm/internal/audio"
	"audionorm/internal/ingest"
	"audionorm/internal/media"
	"context"
	"os/exec"
	"testing"
)

func generateADTS(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5",
		"-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2",
		"-f", "adts", "pipe:1")
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("generate ADTS: %v", err)
	}
	return data
}

func TestPipelineRunsAndResumes(t *testing.T) {
	root := t.TempDir()
	adts := generateADTS(t)

	// Build an ingest result carrying one AAC packet covering the span.
	result := ingest.NewResult()
	result.Packets = append(result.Packets, ingest.Packet{
		Codec: ingest.CodecAAC, ExtSeq: 0, Payload: adts,
		When:       media.Range{Start: 0, End: 24000},
		SampleRate: 48000, Channels: 2,
	})

	p, err := NewPipeline(root)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{
		StreamID: "pipe-test", Range: media.Range{Start: 0, End: 24000},
		Spec: testSpec(), RTPResult: result,
		InputSHA256:   HashBytes(adts),
		DecodeSpec:    audio.StreamSpec{Codec: "aac-lc", Container: "aac", SrcRate: 48000, Channels: 2},
		ContainerData: adts,
	}
	cp, report, err := p.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if cp.Stage != StageSeal || cp.Status != StatusPASS {
		t.Fatalf("stage=%s status=%s, want seal/PASS", cp.Stage, cp.Status)
	}
	if len(report.Recomputed) != 4 {
		t.Fatalf("recomputed stages=%v, want all four", report.Recomputed)
	}
	if cp.Samples <= 0 || cp.Loudness == nil {
		t.Fatal("sealed output missing samples/loudness")
	}

	// Restart: identical input must reuse the sealed checkpoint.
	cp2, report2, err := p.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if report2.ReusedStage != StageSeal || len(report2.Recomputed) != 0 {
		t.Fatalf("resume did not reuse seal: reused=%s recomputed=%v",
			report2.ReusedStage, report2.Recomputed)
	}
	if cp2.Status != StatusPASS {
		t.Fatal("resumed task changed status")
	}
}

func TestPipelineRejectsConflictingInput(t *testing.T) {
	root := t.TempDir()
	adts := generateADTS(t)
	result := ingest.NewResult()
	result.Packets = append(result.Packets, ingest.Packet{
		Codec: ingest.CodecAAC, Payload: adts,
		When: media.Range{Start: 0, End: 24000}, SampleRate: 48000, Channels: 2,
	})
	p, _ := NewPipeline(root)
	in := Input{
		StreamID: "pipe-test", Range: media.Range{Start: 0, End: 24000},
		Spec: testSpec(), RTPResult: result, InputSHA256: HashBytes(adts),
		DecodeSpec:    audio.StreamSpec{Codec: "aac-lc", Container: "aac", SrcRate: 48000, Channels: 2},
		ContainerData: adts,
	}
	if _, _, err := p.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// Same stable key, different input hash -> conflict, no overwrite.
	in.InputSHA256 = HashBytes([]byte("different"))
	if _, _, err := p.Run(context.Background(), in); err == nil {
		t.Fatal("conflicting input must be rejected")
	}
}
