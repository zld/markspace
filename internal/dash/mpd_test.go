package dash

import (
	"bytes"
	"strings"
	"testing"

	"mediadeliver/internal/model"
)

func hasCode(errs []*model.Error, code model.Code) (*model.Error, bool) {
	for _, e := range errs {
		if e.Code == code {
			return e, true
		}
	}
	return nil, false
}

func offsetOf(t *testing.T, data []byte, needle string) int64 {
	t.Helper()
	i := bytes.Index(data, []byte(needle))
	if i < 0 {
		t.Fatalf("fixture does not contain %q", needle)
	}
	return int64(i)
}

const multiMPD = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT2S">
  <Period id="p0" start="PT0S" duration="PT2S">
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="45000" startNumber="1" media="v1080/$Number$.m4s" initialization="v1080/init.mp4"/>
      <Representation id="v1080" bandwidth="5000000"/>
    </AdaptationSet>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="1000" duration="500" startNumber="1" media="v720/$Number$.m4s"/>
      <Representation id="v720" bandwidth="2500000"/>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4">
      <SegmentTemplate timescale="48000" duration="24000" startNumber="1" media="a48/$Number$.m4s"/>
      <Representation id="a48" bandwidth="128000"/>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestParseMultipleTimescales(t *testing.T) {
	mp, err := Parse([]byte(multiMPD), "/fixtures/multi.mpd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(mp.Reps) != 3 {
		t.Fatalf("representations = %d, want 3", len(mp.Reps))
	}
	wantTS := map[string]uint32{"v1080": 90000, "v720": 1000, "a48": 48000}
	for _, r := range mp.Reps {
		if r.Timescale != wantTS[r.ID] {
			t.Errorf("%s timescale = %d, want %d", r.ID, r.Timescale, wantTS[r.ID])
		}
		if len(r.Segments) != 4 {
			t.Fatalf("%s segments = %d, want 4", r.ID, len(r.Segments))
		}
		for i, s := range r.Segments {
			if s.StartNS != int64(i)*500_000_000 {
				t.Errorf("%s[%d] start = %d ns, want %d", r.ID, i, s.StartNS, int64(i)*500_000_000)
			}
			if s.DurationNS != 500_000_000 {
				t.Errorf("%s[%d] duration = %d ns", r.ID, i, s.DurationNS)
			}
		}
		if r.Segments[0].URI != strings.ReplaceAll(mp.Reps[0].Segments[0].URI, "v1080", r.ID) &&
			r.ID != "v1080" {
			t.Errorf("%s uri = %q", r.ID, r.Segments[0].URI)
		}
		if r.Segments[0].URI == "" {
			t.Errorf("%s has empty URI", r.ID)
		}
	}
	if len(mp.Errors) != 0 {
		t.Fatalf("unexpected diagnostics: %v", mp.Errors)
	}
}

const zeroTimescaleMPD = `<MPD type="static" mediaPresentationDuration="PT2S">
 <Period id="p0">
  <AdaptationSet contentType="video">
   <SegmentTemplate timescale="0" duration="1" media="v/$Number$.m4s"/>
   <Representation id="v" bandwidth="1"/>
  </AdaptationSet>
 </Period>
</MPD>`

func TestZeroTimescaleCarriesAttributeOffset(t *testing.T) {
	data := []byte(zeroTimescaleMPD)
	mp, err := Parse(data, "zero.mpd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e, ok := hasCode(mp.Errors, model.CodeZeroTimescale)
	if !ok {
		t.Fatalf("no zero_timescale diagnostic: %v", mp.Errors)
	}
	want := offsetOf(t, data, `timescale="0"`)
	if e.Offset != want {
		t.Fatalf("offset = %d, want %d (attribute start)", e.Offset, want)
	}
	if got := string(data[e.Offset : e.Offset+11]); got != `timescale="` {
		t.Fatalf("bytes at offset = %q", got)
	}
}

const missingIndexMPD = `<MPD type="static" mediaPresentationDuration="PT2S">
 <Period id="p0">
  <AdaptationSet contentType="video">
   <Representation id="v1" bandwidth="1"/>
  </AdaptationSet>
 </Period>
</MPD>`

func TestMissingIndex(t *testing.T) {
	data := []byte(missingIndexMPD)
	mp, err := Parse(data, "missing.mpd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e, ok := hasCode(mp.Errors, model.CodeMissingIndex)
	if !ok {
		t.Fatalf("no missing_index diagnostic: %v", mp.Errors)
	}
	wantToken := `<Representation id="v1"`
	if got := string(data[e.Offset : e.Offset+int64(len(wantToken))]); got != wantToken {
		t.Fatalf("offset %d points at %q, want %q", e.Offset, got, wantToken)
	}
}

func TestTruncatedMPD(t *testing.T) {
	data := []byte(multiMPD)
	cut := len(data) / 2
	mp, err := Parse(data[:cut], "truncated.mpd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !mp.Truncated {
		t.Fatal("truncated MPD not flagged")
	}
	e, ok := hasCode(mp.Errors, model.CodeMPDTruncated)
	if !ok {
		t.Fatalf("no mpd_truncated diagnostic: %v", mp.Errors)
	}
	if e.Offset != int64(cut) {
		t.Fatalf("offset = %d, want %d (end of available input)", e.Offset, cut)
	}
	// Whatever completed before the cut is still usable: the first
	// AdaptationSet and its representation survive.
	if len(mp.Reps) != 1 || mp.Reps[0].ID != "v1080" {
		t.Fatalf("salvaged representations = %+v, want v1080 only", mp.Reps)
	}
	if len(mp.Reps[0].Segments) == 0 {
		t.Fatal("salvaged representation has no segments")
	}

	// A truncation in the XML prologue has nothing to salvage and must
	// still fail hard.
	if _, err := Parse(data[:20], "prologue.mpd"); err == nil {
		t.Fatal("truncated prologue accepted")
	}
}

func TestTimelineExpansionAndOverflow(t *testing.T) {
	ok := `<MPD type="static" mediaPresentationDuration="PT2S"><Period id="p0">
 <AdaptationSet contentType="video"><SegmentTemplate timescale="1000" media="v/$Time$.m4s">
  <SegmentTimeline><S t="0" d="500" r="2"/></SegmentTimeline>
 </SegmentTemplate><Representation id="v" bandwidth="1"/></AdaptationSet></Period></MPD>`
	mp, err := Parse([]byte(ok), "tl.mpd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	segs := mp.Reps[0].Segments
	if len(segs) != 3 {
		t.Fatalf("timeline segments = %d, want 3", len(segs))
	}
	for i, s := range segs {
		if s.StartNS != int64(i)*500_000_000 {
			t.Errorf("segment %d start = %d", i, s.StartNS)
		}
		if s.URI != "v/"+string(rune('0'+i*500/500))+".m4s" && s.URI == "" {
			t.Errorf("segment %d URI empty", i)
		}
	}
	overflow := `<MPD type="static"><Period id="p0"><AdaptationSet contentType="video">
 <SegmentTemplate timescale="1" media="v/$Time$.m4s"><SegmentTimeline>
 <S t="10000000000" d="1" r="1"/></SegmentTimeline></SegmentTemplate>
 <Representation id="v" bandwidth="1"/></AdaptationSet></Period></MPD>`
	mp, err = Parse([]byte(overflow), "overflow.mpd")
	if err != nil {
		t.Fatalf("Parse overflow: %v", err)
	}
	if _, ok := hasCode(mp.Errors, model.CodeTimeOverflow); !ok {
		t.Fatalf("no time_overflow diagnostic: %v", mp.Errors)
	}
}
