package sync

import (
	"errors"
	"testing"

	"mediapipe/internal/clock"
	"mediapipe/internal/mp4"
)

func clockTimes(ts ...int64) []SampleTime {
	out := make([]SampleTime, len(ts))
	for i, t := range ts {
		out[i] = SampleTime{DTS: t, CTS: t, Duration: 3000}
	}
	return out
}

func TestConvertSamplesUnified(t *testing.T) {
	raw := []RawSample{
		{DTS: 0, CTS: 0, Duration: 1024},
		{DTS: 1024, CTS: 1024, Duration: 1024},
	}
	got, unc, err := ConvertSamples(48000, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got[1].DTS != 1920 {
		t.Fatalf("dts %d", got[1].DTS)
	}
	if got[0].Duration != 1920 {
		t.Fatalf("duration %d", got[0].Duration)
	}
	if unc != 2 {
		t.Fatalf("uncertainty %d", unc)
	}
	if _, _, err := ConvertSamples(0, raw); !errors.Is(err, clock.ErrZeroTimescale) {
		t.Fatalf("zero timescale: %v", err)
	}
}

func TestAlignOffsetAndUncertainty(t *testing.T) {
	ref := TrackView{TrackID: 1, Kind: "video", Samples: clockTimes(0, 3000, 6000, 9000)}
	target := TrackView{TrackID: 2, Kind: "audio", Samples: clockTimes(40, 3040, 6040, 9040)}
	a, err := Align(ref, target, AlignConfig{ToleranceTicks: 100, BaseUncertainty: 5})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "aligned" || a.Offset != 40 || a.Matched != 4 || a.Outliers != 0 {
		t.Fatalf("alignment %+v", a)
	}
	if a.Uncertainty < 5 || a.Uncertainty > 20 {
		t.Fatalf("uncertainty %d", a.Uncertainty)
	}
}

func TestAlignReportsOutliers(t *testing.T) {
	ref := TrackView{TrackID: 1, Samples: clockTimes(0, 3000, 6000, 9000, 12000)}
	target := TrackView{TrackID: 2, Samples: clockTimes(0, 3000, 6000, 9000, 26000)}
	a, err := Align(ref, target, AlignConfig{ToleranceTicks: 10})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "outlier" || a.Outliers != 1 {
		t.Fatalf("alignment %+v", a)
	}
	if len(a.Diagnostics) != 1 || a.Diagnostics[0].Code != "align_outlier" {
		t.Fatalf("diagnostics %+v", a.Diagnostics)
	}
}

func TestAlignRejectsNonMonotonic(t *testing.T) {
	ref := TrackView{TrackID: 1, Samples: clockTimes(0, 3000)}
	target := TrackView{TrackID: 2, Samples: clockTimes(50, 40)}
	if _, err := Align(ref, target, AlignConfig{ToleranceTicks: 10}); !errors.Is(err, ErrNonMonotonic) {
		t.Fatalf("non-monotonic accepted: %v", err)
	}
}

func TestAlignUnresolvedAcrossMissingState(t *testing.T) {
	ref := TrackView{TrackID: 1, Samples: clockTimes(0, 3000, 6000)}
	target := TrackView{TrackID: 2, Samples: clockTimes(0, 3000, 6000),
		Missing: []MissingRange{{TrackID: 2, StartTicks: 4000, EndTicks: 5500, Packets: 3, Source: "rtp:lost"}}}
	a, err := Align(ref, target, AlignConfig{ToleranceTicks: 10})
	if !errors.Is(err, ErrMissingState) {
		t.Fatalf("missing state not propagated: %v", err)
	}
	if a.Status != "unresolved" || len(a.Diagnostics) == 0 || a.Diagnostics[0].Code != "transport_gap" {
		t.Fatalf("alignment %+v", a)
	}
}

func TestAlignNoReference(t *testing.T) {
	if _, err := Align(TrackView{TrackID: 1}, TrackView{TrackID: 2, Samples: clockTimes(0)}, AlignConfig{}); !errors.Is(err, ErrNoReference) {
		t.Fatalf("empty ref accepted: %v", err)
	}
}

func TestAlignConfigValidation(t *testing.T) {
	ref := TrackView{TrackID: 1, Samples: clockTimes(0)}
	if _, err := Align(ref, ref, AlignConfig{ToleranceTicks: -1}); err == nil {
		t.Fatal("negative tolerance accepted")
	}
	if _, err := Align(ref, ref, AlignConfig{BaseUncertainty: 999999}); err == nil {
		t.Fatal("huge uncertainty accepted")
	}
}

func TestPropagateMissingMergesOverlaps(t *testing.T) {
	in := []MissingRange{
		{StartTicks: 100, EndTicks: 200, Packets: 1, Source: "a"},
		{StartTicks: 150, EndTicks: 400, Packets: 2, Source: "b"},
		{StartTicks: 500, EndTicks: 600, Packets: 1, Source: "c"},
		{TrackID: 9, StartTicks: 700, EndTicks: 800, Packets: 1},
	}
	out := PropagateMissing(2, in)
	if len(out) != 2 {
		t.Fatalf("merged %+v", out)
	}
	if out[0].StartTicks != 100 || out[0].EndTicks != 400 || out[0].Packets != 3 {
		t.Fatalf("first merge %+v", out[0])
	}
	if out[1].StartTicks != 500 || out[1].TrackID != 2 {
		t.Fatalf("second %+v", out[1])
	}
}

func TestKeyframeHitRate(t *testing.T) {
	if rate := KeyframeHitRate([]int64{0, 100, 200}, []int64{0, 100, 200, 300}); rate != 1 {
		t.Fatalf("rate %f", rate)
	}
	if rate := KeyframeHitRate([]int64{0, 100, 200}, []int64{0, 200}); rate != 2.0/3.0 {
		t.Fatalf("rate %f", rate)
	}
	if rate := KeyframeHitRate(nil, nil); rate != 1 {
		t.Fatalf("empty rate %f", rate)
	}
}

func TestCheckLayoutDetectsDeclarations(t *testing.T) {
	decls := []TrackDecl{
		{Segment: 1, TrackID: 2, Handler: "soun", Channels: 2, SampleRate: 48000, SampleSize: 16},
		{Segment: 2, TrackID: 2, Handler: "soun", Channels: 6, SampleRate: 44100, SampleSize: 16},
		{Segment: 1, TrackID: 1, Handler: "vide", Width: 1280, Height: 720,
			Color: &mp4.Colr{Primaries: 1, Transfer: 1, Matrix: 1}},
		{Segment: 2, TrackID: 1, Handler: "vide", Width: 1280, Height: 720,
			Color: &mp4.Colr{Primaries: 9, Transfer: 1, Matrix: 1}},
	}
	audio := map[uint32]AudioExpectation{2: {Channels: 2, SampleRate: 48000, SampleSize: 16, ChannelLayout: "FL,FR"}}
	video := map[uint32]bool{1: true}
	res := CheckLayout(decls, audio, video)
	codes := map[string]int{}
	for _, d := range res.Issues {
		codes[d.Code]++
		if d.Segment == 0 {
			t.Fatalf("diagnostic not locatable: %+v", d)
		}
	}
	if codes[IssueChannelCount] != 1 || codes[IssueSampleRate] != 1 || codes[IssueColorChange] != 1 {
		t.Fatalf("codes %v issues %+v", codes, res.Issues)
	}
	if res.Tracks != 2 {
		t.Fatalf("tracks %d", res.Tracks)
	}
}

func TestCheckLayoutMissingDeclaration(t *testing.T) {
	decls := []TrackDecl{{Segment: 1, TrackID: 3, Handler: "soun", Channels: 2, SampleRate: 48000}}
	res := CheckLayout(decls, nil, nil)
	if len(res.Issues) != 1 || res.Issues[0].Code != IssueMissingDecl {
		t.Fatalf("issues %+v", res.Issues)
	}
}

func TestDeclsFromFile(t *testing.T) {
	tracks := []mp4.TrackSpec{
		{ID: 1, Handler: "vide", Timescale: 90000, Width: 640, Height: 360,
			Color: &mp4.Colr{Primaries: 1, Transfer: 1, Matrix: 1}},
		{ID: 2, Handler: "soun", Timescale: 48000, Channels: 2, SampleSize: 16, SampleRate: 48000},
	}
	segs := []mp4.SegmentSpec{{Sequence: 1, Samples: []mp4.SampleSpec{
		{TrackID: 1, Duration: 3000, Sync: true, Payload: []byte{1, 2, 3}},
		{TrackID: 2, Duration: 1024, Sync: true, Payload: []byte{4, 5, 6}},
	}}}
	data, err := mp4.BuildFile(tracks, segs)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	decls := DeclsFromFile(f)
	if len(decls) != 2 {
		t.Fatalf("decls %+v", decls)
	}
	res := CheckLayout(decls, map[uint32]AudioExpectation{2: {Channels: 2, SampleRate: 48000, SampleSize: 16}}, map[uint32]bool{1: true})
	if len(res.Issues) != 0 {
		t.Fatalf("unexpected issues %+v", res.Issues)
	}
	gaps := GapDiagnostics(1, []MissingRange{{StartTicks: 1, EndTicks: 2, Packets: 1, Source: "rtp:lost"}})
	if len(gaps) != 1 || gaps[0].Code != IssueMissingGap {
		t.Fatalf("gap diagnostics %+v", gaps)
	}
}
