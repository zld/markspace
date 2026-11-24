package manifest

import (
	"strings"
	"testing"
)

const alignedMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="v0" bandwidth="800000">
        <SegmentTemplate timescale="90000" startNumber="1"
          initialization="init-$RepresentationID$.mp4" media="v0/seg-$Number$.m4s">
          <SegmentTimeline>
            <S t="0" d="90000" r="1"/>
            <S t="180000" d="45000"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v1" bandwidth="1600000">
        <SegmentTemplate timescale="45000" startNumber="1"
          initialization="init-$RepresentationID$.mp4" media="v1/seg-$Number$.m4s">
          <SegmentTimeline>
            <S t="0" d="45000" r="1"/>
            <S t="90000" d="22500"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHAlignedRepresentations(t *testing.T) {
	d, err := ParseDASH(strings.NewReader(alignedMPD))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Representations) != 2 {
		t.Fatalf("reps=%d", len(d.Representations))
	}
	r0 := d.Representations[0]
	if len(r0.Segments) != 3 {
		t.Fatalf("segments=%d want 3 (r=1 gives two)", len(r0.Segments))
	}
	if r0.Segments[0].URI != "v0/seg-1.m4s" || r0.InitURI != "init-v0.mp4" {
		t.Fatalf("template expansion wrong: %+v init=%s", r0.Segments[0], r0.InitURI)
	}
	// 90000 source ticks @90k == 1,000,000 unified ticks.
	if r0.Segments[0].DurationTicks != 1_000_000 {
		t.Fatalf("dur=%d", r0.Segments[0].DurationTicks)
	}
	r1 := d.Representations[1]
	if r1.Segments[2].DurationTicks != 500_000 { // 22500@45k = 0.5s
		t.Fatalf("r1 last dur=%d", r1.Segments[2].DurationTicks)
	}
	if faults := AlignRepresentations(d.Representations, 2); len(faults) != 0 {
		t.Fatalf("aligned reps produced faults: %+v", faults)
	}
}

func TestDASHMisalignmentReported(t *testing.T) {
	mpd := strings.Replace(alignedMPD,
		`<S t="0" d="45000" r="1"/>`,
		`<S t="9000" d="45000" r="1"/>`, 1) // v1 starts 0.2s late @45k
	d, err := ParseDASH(strings.NewReader(mpd))
	if err != nil {
		t.Fatal(err)
	}
	faults := AlignRepresentations(d.Representations, 2)
	if len(faults) == 0 {
		t.Fatal("expected alignment mismatch faults, got none")
	}
	if faults[0].Code != "dash_representation_misaligned" {
		t.Fatalf("code=%s", faults[0].Code)
	}
}

func TestDASHRejectsZeroTimescale(t *testing.T) {
	bad := `<MPD><Period><AdaptationSet><Representation id="x">
	<SegmentTemplate timescale="0" media="$Number$.m4s">
	<SegmentTimeline><S t="0" d="10"/></SegmentTimeline>
	</SegmentTemplate></Representation></AdaptationSet></Period></MPD>`
	if _, err := ParseDASH(strings.NewReader(bad)); err == nil {
		t.Fatal("expected zero timescale error")
	}
}
