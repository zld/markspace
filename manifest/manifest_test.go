package manifest

import (
	"strings"
	"testing"

	"mediadist/timeline"
)

func TestParseMPDTimescaleConversion(t *testing.T) {
	mpdText := `
MPD media-presentation-duration-ms=4000
REP id=low bandwidth=400000 timescale=1000
SEG start=0 duration=2000 uri=low-0.m4s
SEG start=2000 duration=2000 uri=low-1.m4s
REP id=high bandwidth=2000000 timescale=48000
SEG start=0 duration=96000 uri=high-0.m4s
SEG start=96000 duration=96000 uri=high-1.m4s
`
	mpd, err := ParseMPD([]byte(mpdText))
	if err != nil {
		t.Fatalf("ParseMPD: %v", err)
	}
	if len(mpd.Representations) != 2 {
		t.Fatalf("representations = %d, want 2", len(mpd.Representations))
	}
	high := mpd.Representations[1]
	// 96000/48000 s == 2000 ms exactly, without float rounding.
	dur, err := high.Segments[0].Duration.ScaleTo(1)
	if err != nil {
		t.Fatalf("exact scale: %v", err)
	}
	if dur != 2000 {
		t.Fatalf("high rep duration = %d ms, want 2000", dur)
	}
	if high.Segments[1].Start.Cmp(timeline.MustNewRat(2000, 1)) != 0 {
		t.Fatalf("high rep second start = %s, want 2000/1", high.Segments[1].Start)
	}
}

func TestParseMPDRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"INVALID_TIMESCALE":        "REP id=x bandwidth=1 timescale=0\nSEG start=0 duration=1 uri=a\n",
		"ZERO_DURATION_SEGMENT":    "REP id=x bandwidth=1 timescale=1000\nSEG start=0 duration=0 uri=a\n",
		"DUPLICATE_REPRESENTATION": "REP id=x bandwidth=1 timescale=1000\nSEG start=0 duration=1 uri=a\nREP id=x bandwidth=2 timescale=1000\nSEG start=1 duration=1 uri=b\n",
		"MANIFEST_MALFORMED":       "REP id=x bandwidth=1\nSEG start=0 duration=1 uri=a\n",
	}
	for wantCode, text := range cases {
		if _, err := ParseMPD([]byte(text)); timeline.CodeOf(err) != wantCode {
			t.Errorf("input %q: code = %q, want %q (err=%v)", wantCode, timeline.CodeOf(err), wantCode, err)
		}
	}
}

func TestParseHLSDiscontinuityEpochs(t *testing.T) {
	m3u8 := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:100",
		"#EXTINF:2.0,",
		"seg-100.ts",
		"#EXTINF:2.0,",
		"seg-101.ts",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:2.0,",
		"seg-102.ts",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:2.0,",
		"seg-103.ts",
	}, "\n")
	pl, err := ParseM3U8([]byte(m3u8))
	if err != nil {
		t.Fatalf("ParseM3U8: %v", err)
	}
	v := pl.Variants[0]
	if v.MediaSeq != 100 {
		t.Fatalf("media sequence = %d, want 100", v.MediaSeq)
	}
	if len(v.Epochs) != 3 {
		t.Fatalf("epochs = %d, want 3", len(v.Epochs))
	}
	if v.Epochs[1].StartSeq != 102 || v.Epochs[2].StartSeq != 103 {
		t.Fatalf("epoch start sequences wrong: %+v", v.Epochs)
	}
	if err := CrossEpochStitchError(v.Segments[0], v.Segments[1]); err != nil {
		t.Fatalf("same epoch stitch rejected: %v", err)
	}
	if err := CrossEpochStitchError(v.Segments[1], v.Segments[2]); timeline.CodeOf(err) != "CROSS_EPOCH_STITCH" {
		t.Fatalf("cross epoch stitch: code = %q", timeline.CodeOf(err))
	}
}

func TestAlignBoundaryThreshold(t *testing.T) {
	build := func(id string, ts int64, secondStart int64) *Representation {
		return &Representation{ID: id, Timescale: ts, Segments: []Segment{
			{Epoch: 0, Index: 0, Start: ms(0), Duration: ms(2000), URI: id + "-0"},
			{Epoch: 0, Index: 1, Start: ms(secondStart), Duration: ms(2000), URI: id + "-1"},
		}}
	}
	aligner, err := NewAligner(20)
	if err != nil {
		t.Fatalf("NewAligner: %v", err)
	}
	// 8 ms skew at 48 kHz scale: 384/48000 s = 8 ms, inside tolerance.
	ok, err := aligner.Align([]*Representation{
		build("a", 1000, 2000),
		build("b", 48000, 2008),
	})
	if err != nil {
		t.Fatalf("alignment within tolerance rejected: %v", err)
	}
	if ok.MaxDevMs != 8 {
		t.Fatalf("max deviation = %.1f, want 8", ok.MaxDevMs)
	}

	// 25 ms skew exceeds the 20 ms publication threshold.
	_, err = aligner.Align([]*Representation{
		build("a", 1000, 2000),
		build("c", 1000, 2025),
	})
	te, isCoded := timeline.AsError(err)
	if !isCoded || te.Code != "ALIGNMENT_REJECTED" {
		t.Fatalf("want ALIGNMENT_REJECTED, got %v", err)
	}
	for _, key := range []string{"epoch", "segment", "rep_a", "rep_b"} {
		if _, present := te.Fields[key]; !present {
			t.Fatalf("error fields missing %s: %v", key, te.Fields)
		}
	}
}

func TestAlignDetectsMissingSegment(t *testing.T) {
	aligner, _ := NewAligner(20)
	full := &Representation{ID: "full", Segments: []Segment{
		{Index: 0, Start: ms(0), Duration: ms(1000)},
		{Index: 1, Start: ms(1000), Duration: ms(1000)},
	}}
	short := &Representation{ID: "short", Segments: []Segment{
		{Index: 0, Start: ms(0), Duration: ms(1000)},
	}}
	if _, err := aligner.Align([]*Representation{full, short}); timeline.CodeOf(err) != "ALIGNMENT_REJECTED" {
		t.Fatalf("missing segment code = %q", timeline.CodeOf(err))
	}
}

func ms(n int64) timeline.Rat { return timeline.MustNewRat(n, 1) }
