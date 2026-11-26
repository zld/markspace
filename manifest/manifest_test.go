package manifest

import (
	"testing"

	"streamcheck/core"
)

const testMPD = `<?xml version="1.0"?>
<MPD type="static">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v800" bandwidth="800000">
        <SegmentTemplate timescale="1000" startNumber="1" media="v800/$Number$.m4s">
          <SegmentTimeline>
            <S t="0" d="2000" r="3"/>
            <S t="10000" d="2000"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v1600" bandwidth="1600000">
        <SegmentTemplate timescale="1000" startNumber="1" media="v1600/$Number$.m4s">
          <SegmentTimeline>
            <S t="0" d="2000" r="3"/>
            <S t="10000" d="2000"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHTimeline(t *testing.T) {
	tl, err := ParseDASH([]byte(testMPD), "test.mpd")
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Reps) != 2 {
		t.Fatalf("reps: %d", len(tl.Reps))
	}
	rep := tl.Reps[0]
	if rep.ID != "v800" || rep.Bandwidth != 800000 {
		t.Fatalf("rep provenance: %+v", rep)
	}
	// r=3 expands to 4 segments plus the explicit-t one.
	if len(rep.Segments) != 5 {
		t.Fatalf("segments: %d", len(rep.Segments))
	}
	wantStarts := []int64{0, 2000, 4000, 6000, 10000}
	for i, s := range rep.Segments {
		if s.Start.Millis() != wantStarts[i] {
			t.Fatalf("seg %d start %d want %d", i, s.Start.Millis(), wantStarts[i])
		}
		if s.Dur.Millis() != 2000 {
			t.Fatalf("seg %d dur %d", i, s.Dur.Millis())
		}
		if s.URI == "" || s.Index != i+1 {
			t.Fatalf("seg %d provenance: %+v", i, s)
		}
	}
	// The hole between 8000 and 10000 is unexplained: exactly one jump.
	jumps := tl.Analyze()
	if len(jumps) != 2 { // one per representation
		t.Fatalf("jumps: %+v", jumps)
	}
	if jumps[0].Want.Millis() != 8000 || jumps[0].Got.Millis() != 10000 {
		t.Fatalf("jump detail: %+v", jumps[0])
	}
}

func TestCompareRepsAgreeAndDiffer(t *testing.T) {
	tl, err := ParseDASH([]byte(testMPD), "test.mpd")
	if err != nil {
		t.Fatal(err)
	}
	if m := CompareReps(tl.Reps[0], tl.Reps[1]); m != nil {
		t.Fatalf("identical timelines must agree: %+v", m)
	}
	b := tl.Reps[1]
	b.Segments[2].Dur = core.FromMillis(2500)
	m := CompareReps(tl.Reps[0], b)
	if m == nil || m.Field != "duration" || m.Index != 2 {
		t.Fatalf("mismatch: %+v", m)
	}
}

const testPlaylist = `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:40
#EXTINF:2.000,
seg40.ts
#EXTINF:2.000,
seg41.ts
#EXT-X-DISCONTINUITY
#EXTINF:2.000,
seg42.ts
#EXTINF:2.000,
seg43.ts
#EXT-X-ENDLIST
`

func TestParseHLSDiscontinuity(t *testing.T) {
	tl, err := ParseHLS([]byte(testPlaylist), "live.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	rep := tl.Reps[0]
	if len(rep.Segments) != 4 {
		t.Fatalf("segments: %d", len(rep.Segments))
	}
	if rep.Segments[0].Index != 40 || rep.Segments[3].Index != 43 {
		t.Fatalf("media sequence: %+v", rep.Segments)
	}
	if rep.Discontinuities() != 1 {
		t.Fatalf("discontinuities: %d", rep.Discontinuities())
	}
	jumps := tl.Analyze()
	if len(jumps) != 0 {
		t.Fatalf("discontinuity must not be reported as a gap: %+v", jumps)
	}
	if rep.Intervals() != 2 {
		t.Fatalf("intervals: %d", rep.Intervals())
	}
	if !rep.Segments[2].Discontinuity || rep.Segments[2].Interval != 1 {
		t.Fatalf("interval assignment: %+v", rep.Segments[2])
	}
}

func TestParseHLSErrors(t *testing.T) {
	if _, err := ParseHLS([]byte("#EXTINF:2.0,\nx.ts\n"), "x"); err == nil {
		t.Fatal("missing header must fail")
	}
	if _, err := ParseHLS([]byte("#EXTM3U\nseg.ts\n"), "x"); err == nil {
		t.Fatal("URI without EXTINF must fail")
	}
	if _, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:1,\na.ts\n"), "x"); err == nil {
		t.Fatal("unsupported tag must fail")
	}
	if _, err := ParseHLS([]byte("#EXTM3U\n#EXTINF:abc,\na.ts\n"), "x"); err == nil {
		t.Fatal("bad EXTINF must fail")
	}
}

func TestParseDASHErrors(t *testing.T) {
	if _, err := ParseDASH([]byte("<MPD>"), "x"); err == nil {
		t.Fatal("bad XML must fail")
	}
	noReps := `<?xml version="1.0"?><MPD><Period/></MPD>`
	if _, err := ParseDASH([]byte(noReps), "x"); err == nil {
		t.Fatal("no representations must fail")
	}
	negR := `<?xml version="1.0"?><MPD><Period><AdaptationSet><Representation id="a">
	<SegmentTemplate timescale="1000" media="a/$Number$.m4s"><SegmentTimeline>
	<S t="0" d="1000" r="-1"/></SegmentTimeline></SegmentTemplate>
	</Representation></AdaptationSet></Period></MPD>`
	if _, err := ParseDASH([]byte(negR), "x"); err == nil {
		t.Fatal("negative repeat must fail for static segments")
	}
}
