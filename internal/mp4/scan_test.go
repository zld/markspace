package mp4

import (
	"bytes"
	"errors"
	"testing"

	"tracecheck/internal/model"
)

func cleanSpec() VideoSpec {
	return VideoSpec{
		TrackID:       1,
		Timescale:     1000,
		SampleCounts:  []uint32{10},
		SampleDeltas:  []uint32{100},
		SampleSizes:   []uint32{10, 10, 10, 10, 10, 10, 10, 10, 10, 10},
		KeyframeIndex: []uint32{1, 4, 7, 10},
	}
}

func TestScanCleanTrack(t *testing.T) {
	data, err := BuildVideoMP4(cleanSpec())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	tr := res.Tracks[0]
	if tr.Handler != "vide" || tr.Timescale != 1000 || tr.SampleCount != 10 {
		t.Fatalf("track header wrong: %+v", tr)
	}
	if len(tr.Keyframes) != 4 {
		t.Fatalf("keyframes = %d", len(tr.Keyframes))
	}
	// Keyframes at samples 1,4,7,10 with 100ms deltas => 300ms intervals.
	if tr.MaxKeyframeInterval != model.TicksFromMillis(300) {
		t.Fatalf("max interval = %v", tr.MaxKeyframeInterval)
	}
	if len(tr.Findings) != 0 {
		t.Fatalf("clean track must have no findings: %+v", tr.Findings)
	}
}

func TestScanKeyframeTimeline(t *testing.T) {
	data, err := BuildVideoMP4(cleanSpec())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, _ := Scan(bytes.NewReader(data))
	want := []model.Tick{100_000, 400_000, 700_000, 1_000_000}
	for i, kf := range res.Tracks[0].Keyframes {
		if kf.At != want[i] {
			t.Fatalf("keyframe %d at %v, want %v", i, kf.At, want[i])
		}
	}
}

func TestScanBadSTSSIndex(t *testing.T) {
	spec := cleanSpec()
	spec.BadSTSSIndex = 999 // beyond sample count
	data, err := BuildVideoMP4(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	n := model.CountCode(res.Findings, model.ErrMissingIndex)
	if n == 0 {
		t.Fatal("expected missing_index finding for out-of-range stss entry")
	}
	// The valid keyframes are still located despite the bad entry.
	if len(res.Tracks[0].Keyframes) != 4 {
		t.Fatalf("valid keyframes lost: %d", len(res.Tracks[0].Keyframes))
	}
}

func TestScanZeroDeltaTimestamp(t *testing.T) {
	spec := cleanSpec()
	spec.ZeroDelta = true
	data, err := BuildVideoMP4(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n := model.CountCode(res.Findings, model.ErrInvalidTimestamp); n == 0 {
		t.Fatal("expected invalid_timestamp finding for zero delta")
	}
}

func TestScanMissingBoxes(t *testing.T) {
	spec := cleanSpec()
	spec.OmitSTCO = true
	spec.OmitSTSS = true
	data, err := BuildVideoMP4(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if model.CountCode(res.Findings, model.ErrMissingIndex) != 1 {
		t.Fatalf("missing stco must be reported: %+v", res.Findings)
	}
	if model.CountCode(res.Findings, model.ErrNoKeyframes) != 1 {
		t.Fatalf("video track without stss must report no_keyframes: %+v", res.Findings)
	}
}

func TestScanBadChunkOffset(t *testing.T) {
	spec := cleanSpec()
	spec.BadChunkOffset = true
	data, err := BuildVideoMP4(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	n := model.CountCode(res.Findings, model.ErrBrokenChunk)
	if n == 0 {
		t.Fatal("out-of-file chunk offsets must produce broken_chunk findings")
	}
	// Each broken chunk finding must carry a locatable span.
	for _, f := range res.Findings {
		if f.Code == model.ErrBrokenChunk && !f.Span.Valid() {
			t.Fatalf("broken chunk finding lacks a span: %+v", f)
		}
	}
}

func TestScanRejectsNonMP4(t *testing.T) {
	_, err := Scan(bytes.NewReader([]byte("not an mp4 at all")))
	if !errors.Is(err, ErrNotMP4) {
		t.Fatalf("err = %v, want ErrNotMP4", err)
	}
}

func TestScanDeterministic(t *testing.T) {
	data, _ := BuildVideoMP4(cleanSpec())
	first, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Findings) != len(second.Findings) {
		t.Fatal("finding count differs across runs")
	}
	for i := range first.Tracks[0].Keyframes {
		if first.Tracks[0].Keyframes[i] != second.Tracks[0].Keyframes[i] {
			t.Fatal("keyframe timeline differs across runs")
		}
	}
}
