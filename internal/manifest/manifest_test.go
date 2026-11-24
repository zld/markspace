package manifest

import (
	"testing"

	"mediascan/internal/fault"
)

const hlsNormal = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:2.0,
seg10.m4s
#EXTINF:2.0,
seg11.m4s
#EXT-X-DISCONTINUITY
#EXTINF:1.96,
seg12.m4s
`

func TestParseHLSNormal(t *testing.T) {
	m, log := ParseHLS([]byte(hlsNormal))
	if len(m.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(m.Segments))
	}
	if m.StartSeq != 10 || m.Segments[2].MediaSeq != 12 {
		t.Fatalf("sequence mapping wrong: %+v", m.Segments)
	}
	if m.Segments[2].Start != 4.0 || m.Segments[2].End != 5.96 {
		t.Fatalf("time range wrong: %+v", m.Segments[2])
	}
	if !m.Segments[2].Discontinuity {
		t.Fatal("discontinuity flag not propagated")
	}
	if log.HasError() {
		t.Fatalf("unexpected errors: %+v", log.Records())
	}
	disc := log.ByStage(fault.StageManifest)
	var sawDisc bool
	for _, r := range disc {
		if r.Kind == fault.KindDiscontinuity && r.Severity == fault.SeverityWarning {
			sawDisc = true
		}
	}
	if !sawDisc {
		t.Fatal("discontinuity warning missing")
	}
	if m.Segments[0].SourceOffset <= 0 {
		t.Fatal("source offset should point at the URI line")
	}
}

func TestParseHLSMissingAndDuplicate(t *testing.T) {
	playlist := []byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:2,\n#X-MEDIASCAN-SEQ:0\ns0.m4s\n" +
		"#EXTINF:2,\n#X-MEDIASCAN-SEQ:2\ns2.m4s\n" +
		"#EXTINF:2,\n#X-MEDIASCAN-SEQ:2\ns2dup.m4s\n")
	m, log := ParseHLS(playlist)
	if len(m.Segments) != 3 {
		t.Fatalf("segments = %d", len(m.Segments))
	}
	kinds := map[fault.Kind]bool{}
	var offsets []int64
	for _, r := range log.Records() {
		kinds[r.Kind] = true
		offsets = append(offsets, r.SourceOffset)
	}
	if !kinds[fault.KindMissingSegment] || !kinds[fault.KindDuplicateSequence] {
		t.Fatalf("expected missing+duplicate, got %+v", log.Records())
	}
	for _, o := range offsets {
		if o <= 0 {
			t.Fatal("manifest faults must carry source offsets")
		}
	}
}

const dashWithTimeline = `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT6S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v0">
        <SegmentTemplate timescale="1000" media="chunk-$RepresentationID$-$Number$.m4s"
          startNumber="7" duration="2000">
          <SegmentTimeline>
            <S d="2000" r="2"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHTimeline(t *testing.T) {
	m, log := ParseDASH([]byte(dashWithTimeline))
	if len(m.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(m.Segments))
	}
	first := m.Segments[0]
	if first.MediaSeq != 7 || first.URI != "chunk-v0-7.m4s" {
		t.Fatalf("first segment wrong: %+v", first)
	}
	if m.TimeScale != 1000 || first.Duration != 2.0 {
		t.Fatalf("timescale/duration wrong: %+v", first)
	}
	if last := m.Segments[2]; last.End != 6.0 {
		t.Fatalf("last end = %f, want 6", last.End)
	}
	if log.HasError() {
		t.Fatalf("unexpected errors: %+v", log.Records())
	}
}

func TestParseDASHDurationOnly(t *testing.T) {
	mpd := []byte(`<MPD mediaPresentationDuration="PT4S"><Period><AdaptationSet>` +
		`<SegmentTemplate timescale="90000" duration="180000" media="n-$Number$.m4s"/>` +
		`</AdaptationSet></Period></MPD>`)
	m, log := ParseDASH(mpd)
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(m.Segments))
	}
	if m.Segments[0].MediaSeq != 1 || m.Segments[1].MediaSeq != 2 {
		t.Fatal("default startNumber should be 1")
	}
	if log.HasError() {
		t.Fatalf("unexpected errors: %+v", log.Records())
	}
}
