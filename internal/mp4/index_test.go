package mp4

import (
	"context"
	"errors"
	"testing"

	"mediapipe/internal/media"
	"mediapipe/internal/model"
)

func genFile(t *testing.T, spec media.FileSpec) *Index {
	t.Helper()
	data, err := media.BuildFile(spec)
	if err != nil {
		t.Fatalf("build file: %v", err)
	}
	s, err := NewStream(bytesReaderAt(data), int64(len(data)), spec.Asset, Config{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := BuildIndex(context.Background(), s)
	if err != nil {
		t.Fatalf("build index: %v", err)
	}
	return idx
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b)) {
		return 0, errors.New("eof")
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, errors.New("eof")
	}
	return n, nil
}

func binaryTrack() media.FileSpec {
	return media.FileSpec{
		Asset: "clip",
		Tracks: []media.TrackSpec{
			{ID: 1, Handler: "vide", Timescale: 90_000, EditMedia: 3_000, EditDur: 600_000,
				Samples: media.SampleSpec{Count: 10, Size: 100, Delta: 3_000, KeyEvery: 4}},
			{ID: 2, Handler: "soun", Timescale: 48_000, EditMedia: 1_600, EditDur: 480_000,
				Samples: media.SampleSpec{Count: 20, Size: 40, Delta: 1_600}},
		},
	}
}

func TestBuildIndexParsesBothTracks(t *testing.T) {
	idx := genFile(t, binaryTrack())
	if idx.Recovered {
		t.Fatal("clean file must not be reported as recovered")
	}
	if len(idx.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(idx.Tracks))
	}
	v := idx.Tracks[0]
	if v.TrackID != 1 || v.Handler != "vide" || v.Timescale != 90_000 {
		t.Fatalf("video track = %+v", v)
	}
	if v.EditMedia != 3_000 || v.EditDur != 600_000 {
		t.Fatalf("edit list = %d/%d", v.EditMedia, v.EditDur)
	}
	if len(v.Samples) != 10 || !v.Complete {
		t.Fatalf("samples = %d complete=%v", len(v.Samples), v.Complete)
	}
	if v.Samples[0].Offset <= 0 || v.Samples[0].Size != 100 {
		t.Fatalf("sample 0 = %+v", v.Samples[0])
	}
	for i := 1; i < len(v.Samples); i++ {
		if v.Samples[i].Offset != v.Samples[i-1].End() {
			t.Fatalf("samples not contiguous at %d", i)
		}
		if v.Samples[i].DecodeTks != v.Samples[i-1].DecodeTks+3_000 {
			t.Fatalf("dts not advancing at %d", i)
		}
	}
	a := idx.Tracks[1]
	if a.Handler != "soun" || len(a.Samples) != 20 {
		t.Fatalf("audio track = %+v", a)
	}
}

func TestKeyframeLocalisation(t *testing.T) {
	idx := genFile(t, binaryTrack())
	v := idx.Tracks[0]
	// 13.5k ticks lies inside sample 5 (dts 12k..15k), which is a keyframe.
	s, ok := v.Localize(13_500)
	if !ok || s.Index != 5 || s.DecodeTks != 12_000 {
		t.Fatalf("localize = %+v ok=%v", s, ok)
	}
	kf, dev, ok := v.KeyframeBefore(13_500)
	if !ok || kf.Index != 5 {
		t.Fatalf("keyframe = %+v ok=%v", kf, ok)
	}
	if dev != 1_500 {
		t.Fatalf("deviation = %d, want 1500", dev)
	}
	// Sparse keyframes: a tick inside the preceding GOP must still report the
	// actual locating deviation from the previous keyframe (12k ticks away).
	kf, dev, ok = v.KeyframeBefore(11_400)
	if !ok || kf.Index != 1 || dev != 11_400 {
		t.Fatalf("sparse keyframe = %+v dev=%d ok=%v", kf, dev, ok)
	}
	// Before the first keyframe there is nothing to seek to.
	if _, _, ok := v.KeyframeBefore(-1); ok {
		t.Fatal("expected no keyframe before start")
	}
}

func TestRecoverIndexWhenChunkOffsetsMissing(t *testing.T) {
	spec := binaryTrack()
	data, err := media.BuildFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Zero the stco tables so the index is incomplete but recoverable from
	// the sample sizes and the mdat layout.
	data = stripTable(t, data, "stco")
	s, err := NewStream(bytesReaderAt(data), int64(len(data)), "clip", Config{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := BuildIndex(context.Background(), s)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !idx.Recovered {
		t.Fatal("expected recovered index")
	}
	v := idx.Tracks[0]
	if v.Complete || v.Recovered != 10 {
		t.Fatalf("track = complete=%v recovered=%d note=%q", v.Complete, v.Recovered, v.SourceNote)
	}
	s0 := v.Samples[0]
	if s0.Offset <= 0 || s0.Size != 100 {
		t.Fatalf("recovered sample = %+v", s0)
	}
	if err := idx.Validate(int64(len(data)), "clip"); err != nil {
		t.Fatalf("recovered index invalid: %v", err)
	}
}

func TestTruncatedFileFailsWithoutSamples(t *testing.T) {
	spec := binaryTrack()
	spec.Damage = media.DamageTruncate
	data, err := media.BuildFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStream(bytesReaderAt(data), int64(len(data)), "clip", Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildIndex(context.Background(), s)
	if err == nil {
		t.Fatal("expected corruption error for truncated file")
	}
	var me *model.Error
	if !errors.As(err, &me) {
		t.Fatalf("want structured error, got %v", err)
	}
	if me.Kind != model.KindCorrupt || !me.HasOff {
		t.Fatalf("error = %+v", me)
	}
}

// stripTable removes the payload of every box of the given type in place,
// leaving the box header so the scanner still sees a valid container.
func stripTable(t *testing.T, data []byte, typ string) []byte {
	t.Helper()
	for i := 0; i+8 <= len(data); i++ {
		if string(data[i+4:i+8]) != typ {
			continue
		}
		size := int(binaryBE(data[i : i+4]))
		if size < 8 || i+size > len(data) {
			continue
		}
		for j := i + 8; j < i+size; j++ {
			data[j] = 0
		}
		// Break the table length so parsing fails rather than reading
		// an all-zero (but structurally plausible) table.
		data[i+8] = 0xff
	}
	return data
}

func binaryBE(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
