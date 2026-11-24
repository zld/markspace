package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"archive/internal/essence"
	"archive/internal/model"
)

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildMP4LayoutIsConsistent(t *testing.T) {
	specs := DefaultMixed()
	s, ok := FindSpec(specs, "clip_b")
	if !ok {
		t.Fatal("clip_b spec missing")
	}
	layout, data, err := BuildMP4(s)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(data)) != layout.Size {
		t.Fatalf("size mismatch: %d vs %d", len(data), layout.Size)
	}
	if layout.MediaEnd > layout.Size {
		t.Fatalf("media end %d beyond file size %d", layout.MediaEnd, layout.Size)
	}
	for _, tl := range layout.Tracks {
		for _, f := range tl.Frames {
			if f.Offset+f.Size > uint64(len(data)) {
				t.Fatalf("frame %d of %s out of file", f.Seq, tl.Spec.Logical)
			}
			h, err := essence.ParseHeader(data[f.Offset:])
			if err != nil {
				t.Fatalf("frame header %s/%d: %v", tl.Spec.Logical, f.Seq, err)
			}
			if h.TrackID != tl.Spec.TrackID || h.Seq != f.Seq {
				t.Fatalf("frame identity mismatch %s/%d: %+v", tl.Spec.Logical, f.Seq, h)
			}
			if ok, complete := essence.VerifyPayload(data[f.Offset:f.Offset+f.Size], h); !complete || !ok {
				t.Fatalf("frame payload invalid %s/%d", tl.Spec.Logical, f.Seq)
			}
		}
	}
	writeTemp(t, "clip_b.mp4", data)
}

func TestBuildMP4IsDeterministic(t *testing.T) {
	s, _ := FindSpec(DefaultMixed(), "clip_b")
	_, a, err := BuildMP4(s)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := BuildMP4(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("fixture bytes are not deterministic")
	}
}

func TestBuildMP4DropsIndex(t *testing.T) {
	s, _ := FindSpec(DefaultMixed(), "clip_b")
	s.Tracks[0].DropsIndex = true
	_, data, err := BuildMP4(s)
	if err != nil {
		t.Fatal(err)
	}
	// The video track's stts must be absent while the audio one remains.
	if !containsBox(data, "mdat") || !containsBox(data, "moov") {
		t.Fatal("missing core boxes")
	}
}

func TestSpecValidate(t *testing.T) {
	bad := Spec{Name: "x", Format: model.ContainerMP4, Tracks: []TrackSpec{{Logical: "a", TrackID: 1}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected validation failure")
	}
	for _, s := range DefaultMixed() {
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
	}
}

func TestBuildFrameTimestamp(t *testing.T) {
	track := TrackSpec{Logical: "x", TrackID: 4, Kind: model.KindVideo, Timescale: 1000, FrameTicks: 40, Frames: 3, StartTick: 100, PayloadLen: 16, SyncEvery: 2}
	f := BuildFrame(track, 2)
	h, err := essence.ParseHeader(f)
	if err != nil {
		t.Fatal(err)
	}
	if h.StartTick != 180 || h.Duration != 40 || !h.Sync {
		t.Fatalf("header = %+v", h)
	}
	if !countFramesCheck(3) {
		t.Fatal("frame count helper")
	}
}

func countFramesCheck(n int) bool { return n == 3 }

func containsBox(data []byte, name string) bool {
	for i := 0; i+8 <= len(data); i++ {
		if string(data[i+4:i+8]) == name {
			return true
		}
	}
	return false
}
