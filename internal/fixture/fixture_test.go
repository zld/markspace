package fixture

import (
	"errors"
	"testing"

	"replayctl/internal/fault"
	"replayctl/internal/index"
	mp4scan "replayctl/internal/mp4"
)

func TestGeneratedMP4CanBeScannedAndIndexed(t *testing.T) {
	p, err := GenerateSelf(Options{Root: t.TempDir(), Case: CaseOK})
	if err != nil {
		t.Fatal(err)
	}
	mov, err := mp4scan.ScanFile(p.MediaPath)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Build(mov)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Video == nil || idx.Video.Len() != FrameCount {
		t.Fatalf("video index len=%d want %d", idx.Video.Len(), FrameCount)
	}
	if idx.Audio == nil || idx.Audio.Len() != AudioCount {
		t.Fatalf("audio index len=%d want %d", idx.Audio.Len(), AudioCount)
	}
	_, frames := idx.Video.Slice(45_000, 135_000)
	if len(frames) != 25 {
		t.Fatalf("frames=%d want 25", len(frames))
	}
	_, samples := idx.Audio.Slice(24_000, 72_000)
	if len(samples) != 48_000 {
		t.Fatalf("audio samples=%d want 48000", len(samples))
	}
}

func TestTruncatedFixtureReportsOffset16(t *testing.T) {
	p, err := GenerateSelf(Options{Root: t.TempDir(), Case: CaseTruncated})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mp4scan.ScanFile(p.MediaPath)
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("error %T is not fault.Error", err)
	}
	if fe.Code != fault.TruncatedBox || fe.Offset != 16 {
		t.Fatalf("fault=%v code/offset want TRUNCATED_BOX/16", fe)
	}
}
