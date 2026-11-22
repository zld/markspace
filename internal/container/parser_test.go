package container

import (
	"bytes"
	"errors"
	"testing"

	"archivist/internal/model"
	mp4 "github.com/abema/go-mp4"
)

func demoSpec(corruption string) FixtureSpec {
	baseColor := model.ColorMeta{
		Present: true, ColorType: "nclc", Primaries: 1, Transfer: 1, Matrix: 1,
	}
	driftColor := model.ColorMeta{
		Present: true, ColorType: "nclc", Primaries: 9, Transfer: 16, Matrix: 9,
	}
	return FixtureSpec{
		Name: "asset-17", Seed: 17, Brand: "isom", MovieTS: 24000,
		Corruption:   corruption,
		DriftSamples: 3,
		Tracks: []TrackSpec{
			{ID: 1, Kind: model.KindVideo, Timescale: 24000, SampleCount: 60,
				SampleDur: 4000, KeyEvery: 10, Color: baseColor, BitrateLabel: "low"},
			{ID: 2, Kind: model.KindVideo, Timescale: 48000, SampleCount: 120,
				SampleDur: 4000, KeyEvery: 20, Color: driftColor, BitrateLabel: "high"},
			{ID: 3, Kind: model.KindAudio, Timescale: 48000, SampleCount: 240,
				SampleDur: 1000, Channels: 2, SampleRate: 48000},
		},
	}
}

func TestGenerateAndParseRoundTrip(t *testing.T) {
	fix, err := Generate(demoSpec(CorruptNone))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	pf, err := ParseContainer(fix.Data, ParseOptions{Data: fix.Data})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pf.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(pf.Tracks))
	}
	if pf.MovieTimescale != 24000 {
		t.Fatalf("movie timescale = %d", pf.MovieTimescale)
	}
	video := pf.Tracks[0]
	if video.Timescale != 24000 || len(video.Samples) != 60 {
		t.Fatalf("video geometry wrong: ts=%d n=%d", video.Timescale, len(video.Samples))
	}
	if len(video.Edits) != 0 || video.IndexMode != "native" {
		t.Fatalf("video edits/index mode unexpected: %q", video.IndexMode)
	}
	if !video.Color.Present || video.Color.Primaries != 1 {
		t.Fatalf("color meta lost: %+v", video.Color)
	}
	audio := pf.Tracks[2]
	if audio.Audio.Count != 2 || audio.Audio.SampleRate != 48000 || !audio.Audio.Valid {
		t.Fatalf("audio layout wrong: %+v", audio.Audio)
	}
}

func TestGoMp4CrossValidation(t *testing.T) {
	fix, err := Generate(demoSpec(CorruptNone))
	if err != nil {
		t.Fatal(err)
	}
	// The reference library must accept our generated structure and discover
	// exactly the same top-level box sequence.
	var refTypes []mp4.BoxType
	_, err = mp4.ReadBoxStructure(bytes.NewReader(fix.Data), func(h *mp4.ReadHandle) (interface{}, error) {
		if len(h.Path) == 1 {
			refTypes = append(refTypes, h.BoxInfo.Type)
		}
		return h.Expand()
	})
	if err != nil {
		t.Fatalf("go-mp4 rejected fixture: %v", err)
	}
	ours, err := Parse(fix.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(refTypes) != len(ours.Boxes) {
		t.Fatalf("top-level box count: ref=%d ours=%d", len(refTypes), len(ours.Boxes))
	}
	for i, b := range ours.Boxes {
		if b.Type.String() != refTypes[i].String() {
			t.Fatalf("box %d mismatch: %s vs %s", i, b.Type, refTypes[i])
		}
	}
}

func TestCanonicalDigestStable(t *testing.T) {
	fix1, _ := Generate(demoSpec(CorruptNone))
	fix2, _ := Generate(demoSpec(CorruptNone))
	d1 := BoxDigest(mustParse(t, fix1.Data).Root)
	d2 := BoxDigest(mustParse(t, fix2.Data).Root)
	if d1 != d2 {
		t.Fatalf("digest not deterministic:\n%s\n%s", d1, d2)
	}
	if fix1.Digest != fix2.Digest {
		t.Fatal("byte-level fixture digest not deterministic")
	}
}

func TestRejectsZeroTimescale(t *testing.T) {
	fix, err := Generate(demoSpec(CorruptNone))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the mdhd timescale of the first trak to zero (offset discovered
	// structurally, not by magic constant).
	data := cloneBytes(fix.Data)
	root, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	moov := FindFirst(root.Boxes, BoxMoov)
	trak := FindFirst(moov.Children, BoxTrak)
	mdia := FindFirst(trak.Children, BoxMdia)
	mdhd := FindFirst(mdia.Children, BoxMdhd)
	// header + fullbox prefix + body offset 8 (timescale field)
	off := mdhd.Offset + uint64(mdhd.Header) + 4 + 8
	putU32At(data, off, 0)
	_, err = ParseContainer(data, ParseOptions{Data: data})
	if !errors.Is(err, model.ErrZeroTimescale) {
		t.Fatalf("expected zero timescale error, got %v", err)
	}
}

func TestRejectsOutOfBoundsOffset(t *testing.T) {
	fix, err := Generate(demoSpec(CorruptSampleTable))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseContainer(fix.Data, ParseOptions{Data: fix.Data})
	if !errors.Is(err, model.ErrOutOfBoundsOffset) {
		t.Fatalf("expected OOB offset, got %v", err)
	}
}

func TestTruncatedBoxRecoveryAndRejection(t *testing.T) {
	fix, err := Generate(demoSpec(CorruptTruncatedBox))
	if err != nil {
		t.Fatal(err)
	}
	// Strict mode: rejected with evidence.
	if _, err := ParseContainer(fix.Data, ParseOptions{Data: fix.Data}); !errors.Is(err, model.ErrTruncatedBox) {
		t.Fatalf("strict parse should reject truncation, got %v", err)
	}
	// Recovery mode: dangling tail sample trimmed, index rebuilt, continues.
	pf, err := ParseContainer(fix.Data, ParseOptions{Data: fix.Data, AllowTruncatedRecovery: true})
	if err != nil {
		t.Fatalf("recoverable parse failed: %v", err)
	}
	if len(pf.Recovery) == 0 {
		t.Fatal("expected recovery note for truncated box")
	}
	trimmed := pf.Tracks[len(pf.Tracks)-1]
	if len(trimmed.Samples) != 239 {
		t.Fatalf("trimmed samples = %d, want 239 (one dangling tail removed)",
			len(trimmed.Samples))
	}
}

func TestMissingKeyframeRebuild(t *testing.T) {
	fix, err := Generate(demoSpec(CorruptMissingKeyframe))
	if err != nil {
		t.Fatal(err)
	}
	pf, err := ParseContainer(fix.Data, ParseOptions{Data: fix.Data})
	if err != nil {
		t.Fatalf("rebuild failed: %v", err)
	}
	var rebuilt int
	for _, tr := range pf.Tracks {
		if tr.Kind == model.KindVideo && tr.IndexMode == "rebuilt" {
			rebuilt++
			keys := 0
			for _, s := range tr.Samples {
				if s.IsKeyframe {
					keys++
				}
			}
			if keys != 6 {
				t.Fatalf("rebuilt keyframes = %d, want 6", keys)
			}
		}
	}
	if rebuilt != 2 {
		t.Fatalf("rebuilt video tracks = %d, want 2", rebuilt)
	}
}

func TestAudioStartDelayPropagates(t *testing.T) {
	spec := demoSpec(CorruptAudioOffset)
	spec.Tracks[2].StartDelay = 2000
	fix, err := Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := ParseContainer(fix.Data, ParseOptions{Data: fix.Data})
	if err != nil {
		t.Fatal(err)
	}
	audio := pf.Tracks[2]
	if len(audio.Edits) != 1 || audio.StartDelay != 2000 {
		t.Fatalf("audio start delay not preserved: edits=%d delay=%d",
			len(audio.Edits), audio.StartDelay)
	}
}

func TestKeyframeDriftStructure(t *testing.T) {
	fix, err := Generate(demoSpec(CorruptKeyframeDrift))
	if err != nil {
		t.Fatal(err)
	}
	pf, err := ParseContainer(fix.Data, ParseOptions{Data: fix.Data})
	if err != nil {
		t.Fatalf("drift fixture must still parse: %v", err)
	}
	low := pf.Tracks[0]
	high := pf.Tracks[1]
	if low.Samples[0].IsKeyframe != true {
		t.Fatal("low track must keyframe at sample 0")
	}
	if high.Samples[0].IsKeyframe {
		t.Fatal("high track GOP must be shifted away from sample 0")
	}
}

func TestBoundedReadLimits(t *testing.T) {
	// A box header declaring a size beyond EOF must be rejected, not read.
	data := make([]byte, 16)
	putU32At(data, 0, 1000)
	copy(data[4:8], []byte("moov"))
	if _, err := Parse(data); !errors.Is(err, model.ErrTruncatedBox) {
		t.Fatalf("got %v, want truncated box", err)
	}
}

func mustParse(t *testing.T, data []byte) *ParsedFile {
	t.Helper()
	pf, err := ParseContainer(data, ParseOptions{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func putU32At(data []byte, off uint64, v uint32) {
	data[off] = byte(v >> 24)
	data[off+1] = byte(v >> 16)
	data[off+2] = byte(v >> 8)
	data[off+3] = byte(v)
}
