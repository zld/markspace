package wav_test

import (
	"os"
	"path/filepath"
	"testing"

	"media-archiver/internal/media"
	"media-archiver/internal/samplegen"
	"media-archiver/internal/wav"
)

func generate(t *testing.T, inject string) (*samplegen.Expected, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.wav")
	gen, err := samplegen.Generate(samplegen.Options{Seed: 17, Injection: inject, OutputPath: path})
	if err != nil {
		t.Fatalf("generate %s: %v", inject, err)
	}
	return &gen.Expected, path
}

func TestReadValidMultiBlockWAV(t *testing.T) {
	_, path := generate(t, "none")
	doc, err := wav.Read(path, wav.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != samplegen.DemoBlockCount {
		t.Fatalf("blocks=%d want %d", len(doc.Blocks), samplegen.DemoBlockCount)
	}
	if doc.Format.SampleRate != 48000 || doc.Format.Channels != 2 {
		t.Fatalf("unexpected format: %+v", doc.Format)
	}
	if len(doc.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", doc.Issues)
	}
	for i, b := range doc.Blocks {
		if b.CompleteFrames != samplegen.DemoBlockFrames {
			t.Fatalf("block %d frames=%d", i, b.CompleteFrames)
		}
		if !b.TimestampPresent || b.TimestampNS == 0 && i > 0 {
			t.Fatalf("block %d timestamp missing", i)
		}
	}
}

func TestReadTruncatedWAVPreservesExactOffset(t *testing.T) {
	expected, path := generate(t, "truncate")
	doc, err := wav.Read(path, wav.DefaultOptions())
	if err != nil {
		t.Fatalf("tail corruption should be an issue, not fatal: %v", err)
	}
	issue := doc.FirstIssue()
	if issue == nil {
		t.Fatal("expected corruption issue")
	}
	if issue.Code != media.CodeTruncated {
		t.Fatalf("code=%s want %s", issue.Code, media.CodeTruncated)
	}
	if issue.Offset != expected.CorruptOffset {
		t.Fatalf("offset=%d want %d", issue.Offset, expected.CorruptOffset)
	}
	last := doc.Blocks[len(doc.Blocks)-1]
	if last.CorruptOffset != expected.CorruptOffset {
		t.Fatalf("block offset=%d want %d", last.CorruptOffset, expected.CorruptOffset)
	}
	if last.TailBytes == 0 {
		t.Fatal("expected non-empty partial tail to be retained but not padded")
	}
}

func TestReadRejectsUnsupportedChannels(t *testing.T) {
	_, path := generate(t, "none")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// fmt payload starts at RIFF(12) + chunk header(8); channels is bytes 2..3.
	data[22] = 9
	bad := filepath.Join(t.TempDir(), "bad-channels.wav")
	if err := os.WriteFile(bad, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = wav.Read(bad, wav.DefaultOptions())
	me := media.AsError(err)
	if me == nil || me.Code != media.CodeUnsupported {
		t.Fatalf("expected unsupported format error, got %v", err)
	}
	if me.Offset != 20 {
		t.Fatalf("fmt offset=%d want20", me.Offset)
	}
}

func TestReadEnforcesBlockLimit(t *testing.T) {
	_, path := generate(t, "none")
	_, err := wav.Read(path, wav.Options{MaxBlockBytes: 128})
	me := media.AsError(err)
	if me == nil || me.Code != media.CodeBlockTooLarge {
		t.Fatalf("expected block limit error, got %v", err)
	}
}
