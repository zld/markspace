package ingest_test

import (
	"testing"

	"mediacore/ingest"
	"mediacore/internal/sample"
	"mediacore/media"
)

func codeOf(t *testing.T, err error) media.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	return media.CodeOf(err)
}

func TestScanH264Keyframes(t *testing.T) {
	stream, idx := sample.H264(42, 3, 4)
	units, err := ingest.Scan(stream, ingest.CodecH264)
	if err != nil {
		t.Fatal(err)
	}
	var keys int
	for _, u := range units {
		if u.Keyframe {
			keys++
		}
	}
	if keys != 3 {
		t.Fatalf("got %d keyframes, want 3", keys)
	}
	if err := ingest.ValidateIndex(&idx, stream, ingest.CodecH264); err != nil {
		t.Fatalf("valid index rejected: %v", err)
	}
}

func TestScanH265AndAV1(t *testing.T) {
	s265, i265 := sample.H265(7, 2, 3)
	if err := ingest.ValidateIndex(&i265, s265, ingest.CodecH265); err != nil {
		t.Fatalf("h265: %v", err)
	}
	sav1, iav1 := sample.AV1(7, 2, 3)
	if err := ingest.ValidateIndex(&iav1, sav1, ingest.CodecAV1); err != nil {
		t.Fatalf("av1: %v", err)
	}
}

func TestScanCorruptNAL(t *testing.T) {
	// forbidden_zero_bit set
	if c := codeOf(t, scanErr([]byte{0, 0, 0, 1, 0xE5, 1, 2})); c != media.CodeNALCorrupt {
		t.Fatalf("forbidden bit: %s", c)
	}
	// zero-length unit (two start codes back to back)
	if c := codeOf(t, scanErr([]byte{0, 0, 0, 1, 0, 0, 0, 1, 0x65})); c != media.CodeNALCorrupt {
		t.Fatalf("zero length: %s", c)
	}
	// missing start code
	if c := codeOf(t, scanErr([]byte{1, 2, 3})); c != media.CodeNALCorrupt {
		t.Fatalf("no start code: %s", c)
	}
	// truncated AV1 OBU
	if c := codeOf(t, scanErrAV1([]byte{0x32, 10, 1, 2})); c != media.CodeNALCorrupt {
		t.Fatalf("av1 truncated: %s", c)
	}
}

func scanErr(b []byte) error    { _, err := ingest.Scan(b, ingest.CodecH264); return err }
func scanErrAV1(b []byte) error { _, err := ingest.Scan(b, ingest.CodecAV1); return err }

func TestIndexOutOfBounds(t *testing.T) {
	stream, idx := sample.H264(42, 2, 2)
	idx.Offsets[0] = int64(len(stream)) + 10
	if c := codeOf(t, ingest.ValidateIndex(&idx, stream, ingest.CodecH264)); c != media.CodeIndexInvalid {
		t.Fatalf("got %s", c)
	}
}

func TestIndexOverlap(t *testing.T) {
	stream, idx := sample.H264(42, 3, 2)
	idx.Offsets[1] = idx.Offsets[0] + 1 // inside first entry's range
	if c := codeOf(t, ingest.ValidateIndex(&idx, stream, ingest.CodecH264)); c != media.CodeIndexInvalid {
		t.Fatalf("got %s", c)
	}
}

func TestIndexDigestMismatch(t *testing.T) {
	stream, idx := sample.H264(42, 2, 2)
	idx.Digest[0] ^= 0xFF
	err := ingest.ValidateIndex(&idx, stream, ingest.CodecH264)
	if c := codeOf(t, err); c != media.CodeIndexInvalid {
		t.Fatalf("got %s", c)
	}
	if media.ClassOf(media.CodeOf(err)) != media.ClassInput {
		t.Fatal("digest mismatch must be input class")
	}
}

func TestIndexNonKeyframeOffset(t *testing.T) {
	stream, idx := sample.H264(42, 2, 2)
	idx.Offsets[0] += 1 // shifted into payload, no unit starts here
	if c := codeOf(t, ingest.ValidateIndex(&idx, stream, ingest.CodecH264)); c != media.CodeIndexInvalid {
		t.Fatalf("got %s", c)
	}
}

func TestIndexOversizeRejected(t *testing.T) {
	idx := &ingest.KeyframeIndex{Offsets: make([]int64, ingest.MaxIndexEntries+1), Sizes: make([]int64, ingest.MaxIndexEntries+1)}
	if c := codeOf(t, ingest.ValidateIndex(idx, []byte{0}, ingest.CodecH264)); c != media.CodeIndexInvalid {
		t.Fatalf("got %s", c)
	}
}

func TestColorTrackerStates(t *testing.T) {
	var tr ingest.ColorTracker
	if tr.State() != ingest.ColorMissing {
		t.Fatal("initial state must be missing")
	}
	a := ingest.ColorMeta{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"}
	tr.Observe(a, 0, -1)
	if tr.State() != ingest.ColorStable {
		t.Fatal("after first observation must be stable")
	}
	s, err := tr.Summary()
	if err != nil || s == "missing" {
		t.Fatalf("summary: %q %v", s, err)
	}
	b := ingest.ColorMeta{Primaries: "bt2020", Transfer: "pq", Matrix: "bt2020nc"}
	tr.Observe(b, 100, 0)
	if tr.State() != ingest.ColorChanged {
		t.Fatal("new value at later position must be changed")
	}
	tr.Observe(a, 100, 100) // contradiction at same position
	if tr.State() != ingest.ColorConflict {
		t.Fatal("contradiction at same position must be conflict")
	}
	if _, err := tr.Summary(); media.CodeOf(err) != media.CodeColorConflict {
		t.Fatalf("conflict must propagate as error, got %v", err)
	}
}

func TestAudioMetaValidate(t *testing.T) {
	if err := (ingest.AudioMeta{Rate: 44100, Channels: 6, Layout: "5.1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if c := codeOf(t, (ingest.AudioMeta{Rate: 22050, Channels: 2, Layout: "stereo"}).Validate()); c != media.CodeLayoutUnknown {
		t.Fatalf("rate: %s", c)
	}
	if c := codeOf(t, (ingest.AudioMeta{Rate: 48000, Channels: 4, Layout: "quad"}).Validate()); c != media.CodeLayoutUnknown {
		t.Fatalf("layout: %s", c)
	}
	if c := codeOf(t, (ingest.AudioMeta{Rate: 48000, Channels: 2, Layout: "5.1"}).Validate()); c != media.CodeLayoutUnknown {
		t.Fatalf("channel/layout mismatch: %s", c)
	}
}
