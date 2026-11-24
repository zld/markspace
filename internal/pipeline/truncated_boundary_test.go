package pipeline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/rtp"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
	"medpipe/internal/pipeline"
	"medpipe/internal/sample"
)

const testSSRC = 0x99112233
const testStartTS = uint32(0xfffff000)

func makeTestRTP(t *testing.T, i int, truncate bool) sample.Chunk {
	t.Helper()
	seq := uint16(2000 + i)
	payload := ingest.EncodePayload(ingest.FramePayload{
		Flags:           boolByte(i%50 == 0),
		ContentType:     1,
		FrameIndex:      uint32(i),
		DurationSamples: 960,
		Energy:          int32(3000 + i%100),
		PatternID:       uint32(i),
		Content:         make([]byte, 32),
	})
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version: 2, Marker: i%50 == 0, PayloadType: 96, SSRC: testSSRC,
			SequenceNumber: seq, Timestamp: testStartTS + uint32(i)*960,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal frame %d: %v", i, err)
	}
	if truncate {
		raw = raw[:len(raw)-12]
	}
	return sample.Chunk{Kind: sample.ChunkRTP, Data: raw}
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func writeBoundarySource(t *testing.T, dir string, chunks []sample.Chunk) string {
	t.Helper()
	data := sample.WriteContainer(chunks)
	if err := writeFile(dir, pipeline.SourceName, data); err != nil {
		t.Fatal(err)
	}
	digest, err := pipeline.HashFile(filepath.Join(dir, pipeline.SourceName))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writeFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

func TestTruncatedPacketAcrossSegmentBoundaryRecoversConsistently(t *testing.T) {
	chunks := make([]sample.Chunk, 0, 120)
	for i := 0; i < 120; i++ {
		chunks = append(chunks, makeTestRTP(t, i, i == 50))
	}

	fullDir := t.TempDir()
	fullDigest := writeBoundarySource(t, fullDir, chunks)
	opts := pipeline.Options{
		OutDir: fullDir, QueueBytes: 1 << 20, CheckpointEvery: 10,
		SegmentDuration: time.Second, SegmentMaxDuration: time.Second,
	}
	fullRunner := pipeline.NewRunner(opts, sample.Config{SSRC: testSSRC, SampleRate: 48000}, fullDigest)
	full, err := fullRunner.Run(context.Background())
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	if full.ExitCode != 2 || full.ErrorCode != clock.CodeInvalidPacket {
		t.Fatalf("exit=%d code=%s want 2/%s", full.ExitCode, full.ErrorCode, clock.CodeInvalidPacket)
	}
	if full.CorruptPacketSeq != 2050 {
		t.Fatalf("corrupt seq=%d want 2050", full.CorruptPacketSeq)
	}
	if len(full.Segments) < 2 {
		t.Fatalf("segments=%d want at least 2", len(full.Segments))
	}
	seg0 := full.Segments[0]
	if seg0.FrameCount != 50 {
		t.Fatalf("first segment frames=%d want 50; truncated frame entered segment", seg0.FrameCount)
	}
	if got := time.Duration(seg0.EndNS); absDurationDiff(got, time.Second) > 20*time.Millisecond {
		t.Fatalf("boundary error=%v exceeds one audio frame", got-time.Second)
	}

	recoveryDir := t.TempDir()
	recoveryDigest := writeBoundarySource(t, recoveryDir, chunks)
	interruptOpts := opts
	interruptOpts.OutDir = recoveryDir
	interruptOpts.InterruptAfterChunks = 55
	interruptRunner := pipeline.NewRunner(interruptOpts, sample.Config{SSRC: testSSRC, SampleRate: 48000}, recoveryDigest)
	if _, err := interruptRunner.Run(context.Background()); !errors.Is(err, pipeline.ErrInterrupted) {
		t.Fatalf("interrupt run error=%v want ErrInterrupted", err)
	}

	resumeOpts := opts
	resumeOpts.OutDir = recoveryDir
	resumed, err := pipeline.Resume(context.Background(), resumeOpts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.ManifestSHA256 != full.ManifestSHA256 {
		t.Fatalf("manifest mismatch after resume\nfull=%s\nresume=%s", full.ManifestSHA256, resumed.ManifestSHA256)
	}
	if resumed.OutputSHA256 != full.OutputSHA256 {
		t.Fatalf("output checksum mismatch after resume\nfull=%s\nresume=%s", full.OutputSHA256, resumed.OutputSHA256)
	}
	fullManifest, err := os.ReadFile(filepath.Join(fullDir, pipeline.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	resumeManifest, err := os.ReadFile(filepath.Join(recoveryDir, pipeline.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if string(fullManifest) != string(resumeManifest) {
		t.Fatal("manifest bytes differ between uninterrupted and recovered runs")
	}
}

func absDurationDiff(a, b time.Duration) time.Duration {
	if a > b {
		return a - b
	}
	return b - a
}
