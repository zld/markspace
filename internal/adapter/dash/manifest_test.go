package dash

import (
	"bytes"
	"strings"
	"testing"

	"mediaqc/internal/adapter"
	"mediaqc/internal/model"
)

const validMPD = `<?xml version="1.0"?>
<MPD mediaPresentationDuration="PT10S">
  <Period id="p0">
    <AdaptationSet contentType="video">
      <Representation id="v0" mimeType="video/mp4">
        <SegmentTemplate timescale="90000" media="v-$RepresentationID$-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="3000" r="2"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
    <AdaptationSet contentType="audio">
      <Representation id="a0" mimeType="audio/mp4">
        <SegmentTemplate timescaleRatio="30000/1001" timescale="30000" media="a-$Number$.m4s">
          <SegmentTimeline><S t="0" d="1001"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseMultiRepresentation(t *testing.T) {
	p := NewParser(adapter.DefaultLimits())
	m, err := p.Parse("mat-dash-1", bytes.NewReader([]byte(validMPD)), int64(len(validMPD)))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Periods[0].AdaptationSets) != 2 {
		t.Fatal("expected two adaptation sets")
	}
	v := m.Periods[0].AdaptationSets[0].Representations[0]
	if len(v.Segments) != 3 || v.Segments[2].Index != 3 {
		t.Fatalf("r repeat expansion wrong: %+v", v.Segments)
	}
	if v.Segments[1].Start != 3000 || v.Segments[2].Start != 6000 {
		t.Fatal("segment time boundaries must be explicit and monotonic")
	}
	if !strings.Contains(v.Segments[0].Media, "v-v0-1.m4s") {
		t.Fatalf("template expansion wrong: %s", v.Segments[0].Media)
	}
	a := m.Periods[0].AdaptationSets[1].Representations[0]
	if !a.NonInteger || a.Num != 30000 || a.Den != 1001 {
		t.Fatalf("fractional timescale not captured: %+v", a)
	}
	events, errs := m.Events("mat-dash-1")
	if len(events) != 4 || len(errs) != 0 {
		t.Fatalf("events=%d errs=%d", len(events), len(errs))
	}
}

func TestZeroTimescaleRejected(t *testing.T) {
	mpd := strings.Replace(validMPD, `timescale="90000"`, `timescale="0"`, 1)
	_, err := NewParser(adapter.DefaultLimits()).Parse("mat-zero", bytes.NewReader([]byte(mpd)), int64(len(mpd)))
	if err == nil || !model.HasCode(err, model.CodeZeroTimescale) {
		t.Fatalf("want zero-timescale error, got %v", err)
	}
	if model.IsRecoverable(err) {
		t.Fatal("zero timescale must be unrecoverable")
	}
}

func TestDepthAndByteLimits(t *testing.T) {
	lim := adapter.DefaultLimits()
	lim.MaxDepth = 2
	_, err := NewParser(lim).Parse("mat-deep", bytes.NewReader([]byte(validMPD)), int64(len(validMPD)))
	if err == nil || !model.HasCode(err, model.CodeConfig) {
		t.Fatalf("want depth-limit error, got %v", err)
	}
	lim = adapter.DefaultLimits()
	lim.MaxBytes = 50
	_, err = NewParser(lim).Parse("mat-big", bytes.NewReader([]byte(validMPD)), int64(len(validMPD)))
	if err == nil {
		t.Fatal("want byte-limit error")
	}
}

func TestMissingSegmentTemplateRejected(t *testing.T) {
	mpd := `<MPD><Period><AdaptationSet contentType="video">
		<Representation id="x"/></AdaptationSet></Period></MPD>`
	_, err := NewParser(adapter.DefaultLimits()).Parse("mat-notpl", bytes.NewReader([]byte(mpd)), int64(len(mpd)))
	if err == nil || !model.HasCode(err, model.CodeMissingReference) {
		t.Fatalf("want missing-reference error, got %v", err)
	}
}

func TestRegistryRegistration(t *testing.T) {
	got, err := adapter.Default.Lookup(adapter.KindDASH, "mpd-local", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Supports("multi-representation") {
		t.Fatal("dash parser capability missing")
	}
}
