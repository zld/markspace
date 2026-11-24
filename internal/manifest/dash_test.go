package manifest

import (
	"errors"
	"strings"
	"testing"

	"mediarun/internal/fault"
)

const goodMPD = `<?xml version="1.0"?>
<MPD>
  <Period>
    <AdaptationSet>
      <Representation id="v1" bandwidth="800000">
        <SegmentTemplate timescale="90000" initialization="init-$RepresentationID$.mp4"
            media="seg-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="180000" r="1"/>
            <S d="90000"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v2" bandwidth="400000">
        <SegmentTemplate timescale="90000" initialization="init-$RepresentationID$.mp4"
            media="v2/seg-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="180000" r="2"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseMPDGood(t *testing.T) {
	mpd, err := ParseMPD("manifest.mpd", []byte(goodMPD))
	if err != nil {
		t.Fatal(err)
	}
	if len(mpd.Representations) != 2 {
		t.Fatalf("got %d representations", len(mpd.Representations))
	}
	v1 := mpd.Representations[0]
	if v1.ID != "v1" || v1.Bandwidth != 800000 || v1.Timescale != 90000 {
		t.Fatalf("bad v1: %+v", v1)
	}
	if v1.Init != "init-v1.mp4" {
		t.Fatalf("bad init: %s", v1.Init)
	}
	if len(v1.Segments) != 3 {
		t.Fatalf("got %d segments", len(v1.Segments))
	}
	if v1.Segments[0].Duration != 180000 || v1.Segments[2].Duration != 90000 {
		t.Fatalf("bad durations: %+v", v1.Segments)
	}
	if v1.Segments[1].Media != "seg-2.m4s" || v1.Segments[1].Number != 2 {
		t.Fatalf("bad media resolution: %+v", v1.Segments[1])
	}
	if mpd.Representations[1].Segments[0].Media != "v2/seg-1.m4s" {
		t.Fatalf("bad v2 media: %s", mpd.Representations[1].Segments[0].Media)
	}
	for _, s := range v1.Segments {
		if s.Offset <= 0 {
			t.Fatalf("segment missing raw offset: %+v", s)
		}
	}
}

func TestParseMPDZeroTimescale(t *testing.T) {
	bad := strings.Replace(goodMPD, `timescale="90000"`, `timescale="0"`, 1)
	_, err := ParseMPD("m.mpd", []byte(bad))
	if err == nil {
		t.Fatal("expected error")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("want fault.Error, got %T", err)
	}
	if fe.Stage != fault.StageManifest || fe.Track != "v1" || fe.Offset <= 0 {
		t.Fatalf("bad fault: %+v", fe)
	}
	if !strings.Contains(fe.Msg, "timescale") {
		t.Fatalf("bad message: %s", fe.Msg)
	}
}

func TestParseMPDMissingInit(t *testing.T) {
	bad := strings.Replace(goodMPD, ` initialization="init-$RepresentationID$.mp4"`, "", 1)
	_, err := ParseMPD("m.mpd", []byte(bad))
	var fe *fault.Error
	if !errors.As(err, &fe) || !strings.Contains(fe.Msg, "initialization") {
		t.Fatalf("got %v", err)
	}
}

func TestParseMPDBadDuration(t *testing.T) {
	bad := strings.Replace(goodMPD, `d="180000" r="1"`, `d="0" r="1"`, 1)
	_, err := ParseMPD("m.mpd", []byte(bad))
	var fe *fault.Error
	if !errors.As(err, &fe) || !strings.Contains(fe.Msg, "duration") {
		t.Fatalf("got %v", err)
	}
	if fe.Entry != 0 {
		t.Fatalf("entry should be 0, got %d", fe.Entry)
	}
}

func TestParseMPDNoTimeline(t *testing.T) {
	bad := strings.Replace(goodMPD, `<SegmentTimeline>
            <S t="0" d="180000" r="1"/>
            <S d="90000"/>
          </SegmentTimeline>`, "", 1)
	_, err := ParseMPD("m.mpd", []byte(bad))
	if err == nil || !strings.Contains(err.Error(), "SegmentTimeline") {
		t.Fatalf("got %v", err)
	}
}
