package manifest

import (
	"path/filepath"
	"testing"

	"replay/internal/model"
)

func sample() *Manifest {
	return &Manifest{
		Version: Version,
		Set:     "normal",
		Event:   "kill-1",
		Window:  Window{StartMS: 9000, EndMS: 17000},
		Timeline: TimelineInfo{
			Fragments: 10,
			Segments:  []SegmentInfo{{Path: "seg_5.m4s", Seq: 5, StartMS: 8000, DurMS: 2000}},
			MissingMS: [][2]int64{},
		},
		Audio: AudioInfo{InputChannels: 2, InputSampleRate: 48000, OutputChannels: 2, OutputSampleRate: 48000, Layout: "stereo"},
		Video: VideoInfo{ColorTarget: model.ColorInfo{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"}},
		Output: Output{Path: "kill-1.mp4", SHA256: "abc", SizeBytes: 1000, DurationMS: 8000, Bitrate: 1000000},
		Params: Params{WindowBeforeMS: 3000, WindowAfterMS: 5000, TargetBitrate: 1800000, Workers: 2, ParamsHash: "ph"},
		IssueCounts: map[string]int{"COLOR_MISSING": 1},
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	m := sample()
	path := filepath.Join(t.TempDir(), "m.json")
	if err := Write(path, m); err != nil {
		t.Fatal(err)
	}
	if m.Hash == "" {
		t.Fatal("hash must be set after Write")
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Hash != m.Hash {
		t.Fatalf("hash mismatch after round trip: %s vs %s", back.Hash, m.Hash)
	}
	h, err := ComputeHash(back)
	if err != nil {
		t.Fatal(err)
	}
	if h != back.Hash {
		t.Fatal("recomputed hash must match stored hash")
	}
}

func TestHashDeterministicAndSensitive(t *testing.T) {
	a := sample()
	b := sample()
	ha, err := ComputeHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := ComputeHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatal("identical manifests must hash identically")
	}
	b.Window.EndMS++
	hb, _ = ComputeHash(b)
	if ha == hb {
		t.Fatal("hash must change with content")
	}
}

func TestLoadRejectsBadVersion(t *testing.T) {
	m := sample()
	path := filepath.Join(t.TempDir(), "m.json")
	if err := Write(path, m); err != nil {
		t.Fatal(err)
	}
	m.Version = 99
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := Write(bad, m); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("expected version error")
	}
}
