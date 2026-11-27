package ingest

import (
	"testing"

	merr "medialib/internal/errors"
)

func mp4spec(c CorruptKind) MP4Spec {
	return MP4Spec{Timescale: 48000, SampleRate: 48000, Channels: 2,
		Samples: 16, SampleBytes: 20, Delta: 1024, Seed: 3, Corrupt: c}
}

func TestMP4RoundTrip(t *testing.T) {
	data, err := GenMP4(mp4spec(CorruptNone))
	if err != nil {
		t.Fatal(err)
	}
	info, err := ParseMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Timescale != 48000 || info.Channels != 2 || info.SampleCount != 16 {
		t.Fatalf("info=%+v", info)
	}
	if len(info.ChunkOffsets) != 1 || len(info.SampleSizes) != 16 {
		t.Fatalf("index=%+v", info)
	}
}

func TestMP4Truncated(t *testing.T) {
	data, err := GenMP4(mp4spec(CorruptTruncate))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseMP4(data)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	st, _ := merr.StageOf(err)
	off, _ := merr.OffsetOf(err)
	if st != merr.StageParse || off < 0 {
		t.Fatalf("stage=%s off=%d", st, off)
	}
}

func TestMP4BadIndex(t *testing.T) {
	data, err := GenMP4(mp4spec(CorruptBadIndex))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseMP4(data)
	if err == nil {
		t.Fatal("expected index error")
	}
	st, _ := merr.StageOf(err)
	if st != merr.StageVerify {
		t.Fatalf("stage=%s", st)
	}
}

func TestMP4Garbage(t *testing.T) {
	if _, err := ParseMP4([]byte{0, 0, 0}); err == nil {
		t.Fatal("expected header error")
	}
	if _, err := ParseMP4(nil); err == nil {
		t.Fatal("expected empty input error")
	}
}
