package manifest

import (
	"math"
	"strings"
	"testing"

	"mediasync/internal/mederr"
)

const validMPD = `<?xml version="1.0"?>
<MPD type="static">
  <Period>
    <AdaptationSet contentType="video">
      <SegmentTemplate timescale="90000" initialization="init-$RepresentationID$.mp4"
        media="$RepresentationID$/$Number$.m4s">
        <SegmentTimeline>
          <S t="0" d="90000" r="2"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v-hi" />
      <Representation id="v-lo" />
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHBasicMapping(t *testing.T) {
	tl, inits, err := ParseDASHBytes([]byte(validMPD))
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Events) != 6 { // 2 reps x 3 segments
		t.Fatalf("events = %d, want 6", len(tl.Events))
	}
	if len(inits) != 2 || inits[0].URL != "init-v-hi.mp4" {
		t.Fatalf("init refs wrong: %+v", inits)
	}
	hi := tl.Events[0]
	if hi.Representation != "v-hi" || hi.StartNS != 0 || hi.EndNS != 1_000_000_000 {
		t.Fatalf("first event wrong: %+v", hi)
	}
	third := tl.Events[2]
	if third.Segment.Number != 3 || third.StartNS != 2_000_000_000 {
		t.Fatalf("third event wrong: %+v", third)
	}
}

func TestParseDASHZeroTimescale(t *testing.T) {
	mpd := strings.Replace(validMPD, `timescale="90000"`, `timescale="0"`, 1)
	_, _, err := ParseDASHBytes([]byte(mpd))
	if !mederr.HasCode(err, mederr.CodeZeroTimescale) {
		t.Fatalf("want zero_timescale, got %v", err)
	}
}

func TestParseDASHDuplicateSegmentNumber(t *testing.T) {
	mpd := `<?xml version="1.0"?><MPD><Period><AdaptationSet contentType="video">
	  <Representation id="v1" timescale="1000"><SegmentList init="i.mp4">
	  <Segment n="1" t="0" d="1000" byteOffset="0" byteLength="10"/>
	  <Segment n="1" t="1000" d="1000" byteOffset="10" byteLength="10"/>
	  </SegmentList></Representation></AdaptationSet></Period></MPD>`
	_, _, err := ParseDASHBytes([]byte(mpd))
	if !mederr.HasCode(err, mederr.CodeDuplicateNumber) {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestParseDASHArithmeticOverflow(t *testing.T) {
	mpd := `<?xml version="1.0"?><MPD><Period><AdaptationSet contentType="video">
	  <Representation id="v1" timescale="1"><SegmentList init="i.mp4">
	  <Segment n="1" t="` + "9223372036854775807" + `" d="10" byteOffset="0" byteLength="10"/>
	  </SegmentList></Representation></AdaptationSet></Period></MPD>`
	_, _, err := ParseDASHBytes([]byte(mpd))
	if !mederr.HasCode(err, mederr.CodeArithmeticOverflow) {
		t.Fatalf("want overflow error, got %v", err)
	}
}

func TestParseDASHMalformedAndEmpty(t *testing.T) {
	for _, in := range [][]byte{
		[]byte("not xml at all"),
		[]byte(`<MPD></MPD>`),
		[]byte(`<?xml version="1.0"?><MPD><Period/></MPD>`),
	} {
		if _, _, err := ParseDASHBytes(in); err == nil {
			t.Fatalf("input %q must fail", in)
		}
	}
}

func TestParseDASHSegmentListByteRanges(t *testing.T) {
	mpd := `<?xml version="1.0"?><MPD><Period><AdaptationSet contentType="audio">
	  <Representation id="a1" timescale="48000"><SegmentList init="a.mp4">
	  <Segment n="7" t="0" d="48000" byteOffset="4096" byteLength="2048"/>
	  </SegmentList></Representation></AdaptationSet></Period></MPD>`
	tl, _, err := ParseDASHBytes([]byte(mpd))
	if err != nil {
		t.Fatal(err)
	}
	ev := tl.Events[0]
	if ev.Track != TrackAudio || ev.Segment.ByteOffset != 4096 ||
		ev.Segment.ByteLength != 2048 || ev.Segment.Number != 7 {
		t.Fatalf("segment ref wrong: %+v", ev.Segment)
	}
	if math.Abs(float64(ev.EndNS-ev.StartNS-1_000_000_000)) > 1 {
		t.Fatalf("duration mapping wrong: %+v", ev)
	}
}

const validHLS = `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:2.0,
seg10.ts
#EXT-X-DISCONTINUITY
#EXTINF:1.5,
seg11.ts
#EXTINF:2.0,
seg12.ts
#EXT-X-DISCONTINUITY
#EXTINF:2.0,
seg13.ts`

func TestParseHLSDiscontinuitiesAndIsolation(t *testing.T) {
	tl, err := ParseHLS(strings.NewReader(validHLS), 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Events) != 4 {
		t.Fatalf("events = %d", len(tl.Events))
	}
	if tl.Events[0].Segment.Number != 10 || tl.Events[3].Segment.Number != 13 {
		t.Fatal("media sequence numbering wrong")
	}
	// Global timeline remains continuous across the local clock resets.
	for i := 1; i < len(tl.Events); i++ {
		if tl.Events[i].StartNS != tl.Events[i-1].EndNS {
			t.Fatalf("global timeline gap at %d", i)
		}
	}
	if !tl.Events[1].Isolated || tl.Events[1].DiscoSeq != 0 {
		t.Fatalf("first post-disco segment must be isolated: %+v", tl.Events[1])
	}
	if !tl.Events[3].Isolated || tl.Events[0].Isolated || tl.Events[2].Isolated {
		t.Fatalf("isolation flags wrong: %+v", tl.Events)
	}
	if !tl.Events[1].Discontinuity || !tl.Events[3].Discontinuity {
		t.Fatal("discontinuity flags missing")
	}
}

func TestParseHLSMalformed(t *testing.T) {
	bad := []string{
		"",
		"#EXTINF:2.0,\nseg.ts\n",
		"#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:abc\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXTINF:x,\na.ts\n",
		"#EXTM3U\n#EXTINF:1.0,\n",
	}
	for _, b := range bad {
		if _, err := ParseHLS(strings.NewReader(b), 90000); err == nil {
			t.Fatalf("playlist %q must fail", b)
		}
	}
	if _, err := ParseHLS(strings.NewReader(validHLS), 0); err == nil {
		t.Fatal("zero timescale HLS must fail")
	}
}

func TestExitCodeClassification(t *testing.T) {
	_, _, err := ParseDASHBytes([]byte("nope"))
	if got := mederr.ExitCode(err); got != mederr.ExitInvalidInput {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if got := mederr.ExitCode(nil); got != mederr.ExitOK {
		t.Fatalf("nil error exit = %d", got)
	}
}
