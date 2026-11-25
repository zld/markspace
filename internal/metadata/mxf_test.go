package metadata

import (
	"fmt"
	"strings"
	"testing"

	"mediasync/internal/model"
)

const sampleMXF = `MXF1
LENGTH=%d
TIMESCALE=1000
DURATION=2000
ROTATION=90
CROP=10,20,30,40
COLOR=9,16,9,1
TRACK=kind=video id=1 timescale=30 duration=60 width=1920 height=1080 rotation=90 crop=10,20,30,40 color=9,16,9,1
TRACK=kind=audio id=2 timescale=48000 duration=96000 channels=2 samplerate=48000
TRACK=kind=subtitle id=3 timescale=1000 duration=2000 language=zh
END
`

// happySample returns sampleMXF with a LENGTH matching its real byte size.
// The LENGTH field width is padded so rewriting its value cannot change the
// total byte count.
func happySample() []byte {
	raw := []byte(strings.Replace(sampleMXF, "LENGTH=%d", "LENGTH=0000", 1))
	n := len(raw)
	copy(raw[len("MXF1\nLENGTH="):], []byte(fmt.Sprintf("%04d", n)))
	return raw
}

func TestParseMXFHappyPath(t *testing.T) {
	rec := model.NewRecorder()
	raw := happySample()
	meta, err := ParseMXF(raw, rec)
	if err != nil || rec.HasError() {
		t.Fatalf("ParseMXF failed: %v records=%v", err, rec.Records())
	}
	if meta.Source != SourceMXF || meta.DeclaredSize != int64(len(raw)) {
		t.Fatalf("header fields wrong: source=%s length=%d want=%d",
			meta.Source, meta.DeclaredSize, len(raw))
	}
	// 2000 ms -> 180000 master ticks.
	if meta.DurationTicks != 180000 {
		t.Fatalf("duration ticks = %d, want 180000", meta.DurationTicks)
	}
	if len(meta.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(meta.Tracks))
	}
	v := meta.Tracks[0]
	if v.Kind != KindVideo || v.Width != 1920 || v.Rotation != 90 {
		t.Fatalf("video track wrong: %+v", v)
	}
	// 60 frames at 30 fps = 2 s -> 180000 ticks.
	if v.DurationTicks != 180000 {
		t.Fatalf("video duration ticks = %d, want 180000", v.DurationTicks)
	}
	// 96000/48000 = 2 s.
	if meta.Tracks[1].DurationTicks != 180000 {
		t.Fatalf("audio duration ticks = %d, want 180000", meta.Tracks[1].DurationTicks)
	}
	if meta.Tracks[2].Language != "zh" {
		t.Fatalf("subtitle language = %q", meta.Tracks[2].Language)
	}
}

func TestParseMXFTruncated(t *testing.T) {
	bad := []byte("MXF1\nLENGTH=99999\nTIMESCALE=1000\nDURATION=100\nEND\n")
	rec := model.NewRecorder()
	_, err := ParseMXF(bad, rec)
	if err == nil {
		t.Fatal("truncated MXF must return error")
	}
	recs := rec.Records()
	if len(recs) != 1 || recs[0].Code != model.ErrTruncatedHeader {
		t.Fatalf("want one TRUNCATED_HEADER, got %v", recs)
	}
	if recs[0].Offset != int64(len(bad)) {
		t.Fatalf("offset = %d, want %d", recs[0].Offset, len(bad))
	}
}

func TestParseMXFBadMagicAndTimestamps(t *testing.T) {
	rec := model.NewRecorder()
	if _, err := ParseMXF([]byte("XXXX\n"), rec); err == nil {
		t.Fatal("bad magic must fail")
	}
	if rec.Records()[0].Code != model.ErrTruncatedHeader {
		t.Fatalf("want truncated-header record, got %v", rec.Records())
	}

	rec2 := model.NewRecorder()
	illegal := "MXF1\nTIMESCALE=0\nDURATION=500\nTRACK=kind=audio id=1 timescale=1000 duration=-5\nEND\n"
	_, err := ParseMXF([]byte(illegal), rec2)
	if err == nil {
		t.Fatal("illegal timestamps must fail")
	}
	codes := map[model.ErrorCode]bool{}
	for _, r := range rec2.Records() {
		codes[r.Code] = true
	}
	if !codes[model.ErrInvalidTimestamp] {
		t.Fatalf("want INVALID_TIMESTAMP records, got %v", rec2.Records())
	}
}

func TestAdjudicatePriorityAndConflict(t *testing.T) {
	rec := model.NewRecorder()
	mxf := &ContainerMeta{Source: SourceMXF, Timescale: 1000, DurationTicks: 180000,
		Rotation: 90, Color: ColorInfo{ColorType: "nclx", Primaries: 9, Transfer: 16, Matrix: 9},
		Declared: map[string]bool{"duration": true, "rotation": true, "color": true}}
	mp4 := &ContainerMeta{Source: SourceMP4, Timescale: 90000, DurationTicks: 180000,
		Rotation: 0, Color: ColorInfo{ColorType: "nclx", Primaries: 1, Transfer: 1, Matrix: 1},
		Declared: map[string]bool{"duration": true, "rotation": true, "color": true}}
	v := Adjudicate(mxf, mp4, DefaultDefaults(), rec)

	if v.Rotation.Value != 90 || v.Rotation.Winner != SourceMXF || !v.Rotation.Conflict {
		t.Fatalf("rotation verdict wrong: %+v", v.Rotation)
	}
	if v.Color.Winner != SourceMXF || !v.Color.Conflict {
		t.Fatalf("color verdict wrong: %+v", v.Color)
	}
	// Equal values must not be flagged as conflicts.
	if v.DurationTick.Conflict || v.DurationTick.Winner != SourceMXF {
		t.Fatalf("duration verdict wrong: %+v", v.DurationTick)
	}
	// Only rotation and color conflict; equal duration must not be flagged.
	if n := len(rec.Records()); n != 2 {
		t.Fatalf("want 2 conflict warnings, got %d: %v", n, rec.Records())
	}
	for _, r := range rec.Records() {
		if r.Severity != model.SevWarn {
			t.Fatalf("metadata conflicts must be warnings: %v", r)
		}
	}
}

func TestAdjudicateFallbackAndDefault(t *testing.T) {
	rec := model.NewRecorder()
	// MXF declares nothing -> MP4 wins; MP4 also empty -> default wins.
	v := Adjudicate(&ContainerMeta{Source: SourceMXF, Declared: map[string]bool{}},
		&ContainerMeta{Source: SourceMP4, Rotation: 270, Declared: map[string]bool{"rotation": true}},
		DefaultDefaults(), rec)
	if v.Rotation.Value != 270 || v.Rotation.Winner != SourceMP4 {
		t.Fatalf("want MP4 fallback, got %+v", v.Rotation)
	}
	if v.Color.Winner != SourceDefault || v.Color.Conflict {
		t.Fatalf("want default color without conflict, got %+v", v.Color)
	}
}

func TestAdjudicateTrackMatching(t *testing.T) {
	rec := model.NewRecorder()
	mxf := &ContainerMeta{Source: SourceMXF, Declared: map[string]bool{}, Tracks: []TrackMeta{
		{Kind: KindVideo, TrackID: 7, DurationTicks: 90000, Declared: map[string]bool{"duration": true}},
	}}
	mp4 := &ContainerMeta{Source: SourceMP4, Declared: map[string]bool{}, Tracks: []TrackMeta{
		{Kind: KindVideo, TrackID: 1, DurationTicks: 90000, Declared: map[string]bool{"duration": true}},
		{Kind: KindAudio, TrackID: 2, DurationTicks: 45000, Declared: map[string]bool{"duration": true}},
	}}
	v := Adjudicate(mxf, mp4, DefaultDefaults(), rec)
	if len(v.Tracks) != 2 {
		t.Fatalf("want 2 track verdicts, got %d", len(v.Tracks))
	}
	tv := v.Tracks[0]
	if tv.Kind != KindVideo || tv.MXFID != 7 || tv.MP4ID != 1 {
		t.Fatalf("matched track wrong: %+v", tv)
	}
	if v.Tracks[1].Kind != KindAudio || v.Tracks[1].MXFID != 0 || v.Tracks[1].MP4ID != 2 {
		t.Fatalf("mp4-only track wrong: %+v", v.Tracks[1])
	}
}
