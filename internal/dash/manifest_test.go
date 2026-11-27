package dash

import (
	"testing"

	"mediarc/internal/model"
)

const validMPD = `<?xml version="1.0"?>
<MPD mediaPresentationDuration="PT4S">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="video-low" bandwidth="400000" timescale="1000">
        <SegmentTemplate media="low-$Number$.m4s" startNumber="1"/>
        <SegmentTimeline>
          <S t="0" d="2000"/>
          <S t="2000" d="2000"/>
        </SegmentTimeline>
      </Representation>
      <Representation id="video-high" bandwidth="1200000" timescale="1000">
        <SegmentTemplate media="high-$Number$.m4s" startNumber="1"/>
        <SegmentTimeline>
          <S t="0" d="2000"/>
          <S t="2000" d="2000"/>
        </SegmentTimeline>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestParseValidMPD(t *testing.T) {
	reps, global, err := Parse("sample.mpd", []byte(validMPD))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(global) != 0 {
		t.Fatalf("unexpected global errors: %v", global)
	}
	if len(reps) != 2 {
		t.Fatalf("reps=%d want 2", len(reps))
	}
	for _, r := range reps {
		if len(r.Segments) != 2 || r.Format != "dash" || r.Timescale != 1000 {
			t.Fatalf("representation %+v not parsed correctly", r)
		}
		if len(r.ManifestErrors) != 0 {
			t.Fatalf("unexpected rep errors: %v", r.ManifestErrors)
		}
	}
	if reps[0].Segments[0].URL != "low-1.m4s" || reps[1].Segments[1].URL != "high-2.m4s" {
		t.Fatalf("template expansion wrong: %+v", reps)
	}
}

func TestParseMissingDuration(t *testing.T) {
	mpd := []byte(`<MPD mediaPresentationDuration="PT2S">
  <Period><Representation id="r1">
    <SegmentTimeline>
      <S t="0"/>
    </SegmentTimeline>
  </Representation></Period>
</MPD>`)
	reps, _, err := Parse("x.mpd", mpd)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(reps) != 1 || !hasKind(reps[0].ManifestErrors, model.KindMissingDuration) {
		t.Fatalf("missing-duration error not attached: %+v", reps)
	}
	e := reps[0].ManifestErrors[0]
	if e.Line != 4 {
		t.Fatalf("line=%d want 4", e.Line)
	}
}

func TestParseDuplicateReference(t *testing.T) {
	// A SegmentTemplate with a static media name (no $Number$) makes every S
	// resolve to the same URL; the second and third reference are duplicates.
	mpd := []byte(`<MPD mediaPresentationDuration="PT3S">
  <Representation id="r1">
    <SegmentTemplate media="fixed.m4s" startNumber="1"/>
    <S t="0" d="1000"/>
    <S t="1000" d="1000"/>
    <S t="2000" d="1000"/>
  </Representation>
</MPD>`)
	reps, _, err := Parse("dup.mpd", mpd)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(reps[0].Segments) != 1 {
		t.Fatalf("only the first reference should be kept, got %d", len(reps[0].Segments))
	}
	if countKind(reps[0].ManifestErrors, model.KindDuplicateReference) != 2 {
		t.Fatalf("two duplicate references expected: %v", reps[0].ManifestErrors)
	}
	e := reps[0].ManifestErrors[0]
	if e.Line != 5 {
		t.Fatalf("duplicate line=%d want 5", e.Line)
	}
}

func TestParseRangeOutOfBounds(t *testing.T) {
	mpd := []byte(`<MPD mediaPresentationDuration="PT2S">
  <Representation id="r1" timescale="1000">
    <S t="1500" d="1000"/>
  </Representation>
</MPD>`)
	reps, _, err := Parse("range.mpd", mpd)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasKind(reps[0].ManifestErrors, model.KindRangeOutOfBounds) {
		t.Fatalf("range error not detected: %v", reps[0].ManifestErrors)
	}
}

func TestParseUnknownTag(t *testing.T) {
	mpd := []byte(`<MPD mediaPresentationDuration="PT1S">
  <MysteryTag foo="bar"/>
</MPD>`)
	_, global, err := Parse("u.mpd", mpd)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasKind(global, model.KindManifestSyntax) {
		t.Fatalf("unknown tag not classified: %v", global)
	}
	if global[0].Line != 2 {
		t.Fatalf("line=%d want 2", global[0].Line)
	}
}

func TestParseTruncated(t *testing.T) {
	mpd := []byte(`<MPD mediaPresentationDuration="PT1S">
  <Period>
    <Representation id="r1">
`)
	if _, _, err := Parse("trunc.mpd", mpd); err == nil {
		t.Fatal("truncated manifest must return a parse error")
	}
	mpdBad := []byte(`<MPD><Period></MPD></Period>`)
	if _, _, err := Parse("bad.mpd", mpdBad); err == nil {
		t.Fatal("mismatched closing tags must return a parse error")
	}
}

func TestParseMPDMissingDuration(t *testing.T) {
	mpd := []byte(`<MPD>
  <Representation id="r1"><S t="0" d="1000"/></Representation>
</MPD>`)
	_, global, err := Parse("d.mpd", mpd)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasKind(global, model.KindMissingDuration) {
		t.Fatalf("MPD-level missing duration not reported: %v", global)
	}
}

func TestISODuration(t *testing.T) {
	cases := map[string]int64{"PT4S": 4000, "PT1.500S": 1500, "PT0.25S": 250, "P1D": -1}
	for in, want := range cases {
		if got := isoDurationMillis(in); got != want {
			t.Fatalf("isoDurationMillis(%q)=%d want %d", in, got, want)
		}
	}
}

func hasKind(errs []*model.MediaError, kind model.ErrorKind) bool {
	return countKind(errs, kind) > 0
}

func countKind(errs []*model.MediaError, kind model.ErrorKind) int {
	n := 0
	for _, e := range errs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}
