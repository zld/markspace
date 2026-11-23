package align

import (
	"fmt"
	"strings"
	"testing"

	"mediadeliver/internal/dash"
	"mediadeliver/internal/hls"
	"mediadeliver/internal/model"
	"mediadeliver/internal/segment"
)

func genMPD(periodStart, periodDur string) string {
	return fmt.Sprintf(`<MPD type="static" mediaPresentationDuration="%s">
 <Period id="p0" start="%s" duration="%s">
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <SegmentTemplate timescale="90000" duration="45000" startNumber="1" media="v1080/$Number$.m4s"/>
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
</MPD>`, periodDur, periodStart, periodDur)
}

func mustMPD(t *testing.T, text string) *dash.MPD {
	t.Helper()
	mp, err := dash.Parse([]byte(text), "fixture.mpd")
	if err != nil {
		t.Fatalf("mpd parse: %v", err)
	}
	return mp
}

const fourSecondPlaylist = `#EXTM3U
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:1
#EXT-X-DISCONTINUITY-SEQUENCE:0
#EXTINF:4.000,
1.m4s
#EXTINF:4.000,
2.m4s
#EXT-X-ENDLIST
`

const discontinuityTwicePlaylist = `#EXTM3U
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:1
#EXT-X-DISCONTINUITY-SEQUENCE:7
#EXTINF:4.000,
1.m4s
#EXT-X-DISCONTINUITY
#EXTINF:4.000,
2.m4s
#EXTINF:4.000,
3.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.000,
4.m4s
#EXT-X-ENDLIST
`

func mustPL(t *testing.T, text string) *hls.Playlist {
	t.Helper()
	p, err := hls.Parse([]byte(text), "fixture.m3u8")
	if err != nil {
		t.Fatalf("hls parse: %v", err)
	}
	return p
}

func TestAlignmentInvariants(t *testing.T) {
	mp := mustMPD(t, genMPD("PT0S", "PT8S"))
	pl := mustPL(t, fourSecondPlaylist)
	m := Build(Input{MPD: mp, Playlist: pl})
	if len(m.Representations) != 3 {
		t.Fatalf("representations = %d, want 3", len(m.Representations))
	}
	if len(m.Intervals) != 2 {
		t.Fatalf("intervals = %d, want 2", len(m.Intervals))
	}
	if m.Summary.MaxDriftNS != 0 {
		t.Fatalf("max drift = %d, want 0", m.Summary.MaxDriftNS)
	}
	if !m.Summary.Deliverable || len(m.Errors) != 0 {
		t.Fatalf("manifest not deliverable: %v", m.Errors)
	}
	prevEnd := int64(-1)
	for i, iv := range m.Intervals {
		if iv.StartNS < prevEnd {
			t.Fatalf("interval %d starts at %d before previous end %d", i, iv.StartNS, prevEnd)
		}
		if iv.StartNS >= iv.EndNS {
			t.Fatalf("interval %d has non-positive span", i)
		}
		for _, e := range iv.Entries {
			if e.EndNS <= e.StartNS {
				t.Fatalf("interval %d entry %s non-positive span", i, e.RepresentationID)
			}
			if e.DriftNS > MaxDriftNS {
				t.Fatalf("entry drift %d over tolerance", e.DriftNS)
			}
		}
		prevEnd = iv.EndNS
	}
}

func TestDiscontinuityKeepsIndependentIntervals(t *testing.T) {
	// First interval 0..4s, second 4..12s, third 12..14s. Different
	// discontinuity sequences must not be merged or re-ordered.
	mp := mustMPD(t, genMPD("PT0S", "PT14S"))
	pl := mustPL(t, discontinuityTwicePlaylist)
	m := Build(Input{MPD: mp, Playlist: pl})
	if len(m.Intervals) != 4 {
		t.Fatalf("intervals = %d, want 4", len(m.Intervals))
	}
	want := []struct {
		start, end int64
		seq        uint64
	}{
		{0, 4_000_000_000, 7},
		{4_000_000_000, 8_000_000_000, 8},
		{8_000_000_000, 12_000_000_000, 8},
		{12_000_000_000, 14_000_000_000, 9},
	}
	for i, w := range want {
		iv := m.Intervals[i]
		if iv.StartNS != w.start || iv.EndNS != w.end || iv.DiscontinuitySeq != w.seq {
			t.Fatalf("interval %d = [%d,%d) seq %d, want [%d,%d) seq %d",
				i, iv.StartNS, iv.EndNS, iv.DiscontinuitySeq, w.start, w.end, w.seq)
		}
	}
	if m.Intervals[1].HLSIntervalStartNS != 4_000_000_000 {
		t.Fatalf("second interval start = %d", m.Intervals[1].HLSIntervalStartNS)
	}
	if m.Intervals[3].IntervalOffsetNS != 0 {
		t.Fatalf("post-discontinuity offset not reset: %d", m.Intervals[3].IntervalOffsetNS)
	}
}

func TestBoundaryDriftMarksNonDeliverable(t *testing.T) {
	// DASH segments are 0.5 s; the HLS playlist claims 0.6 s ones. The
	// shared boundaries drift by 100 ms, far over the 1 ms tolerance.
	mp := mustMPD(t, genMPD("PT0S", "PT8S"))
	pl := mustPL(t, "#EXTM3U\n#EXTINF:0.600,\n1.m4s\n#EXTINF:0.600,\n2.m4s\n#EXT-X-ENDLIST\n")
	m := Build(Input{MPD: mp, Playlist: pl})
	if m.Summary.Deliverable {
		t.Fatal("manifest with drift reported deliverable")
	}
	if m.Summary.MaxDriftNS <= MaxDriftNS {
		t.Fatalf("max drift = %d, want > %d", m.Summary.MaxDriftNS, MaxDriftNS)
	}
	drift := false
	for _, e := range m.Errors {
		if e.Code == model.CodeBoundaryDrift {
			drift = true
			if e.Offset == model.NoOffset {
				t.Fatal("boundary drift error has no source offset")
			}
		}
	}
	if !drift {
		t.Fatalf("no boundary_drift diagnostic: %v", m.Errors)
	}
	for _, iv := range m.Intervals {
		for _, e := range iv.Entries {
			if e.DriftNS > MaxDriftNS && e.Deliverable {
				t.Fatalf("drifting entry marked deliverable: %+v", e)
			}
		}
	}
}

func TestScanCorruptionPropagates(t *testing.T) {
	// 4 s DASH segments so each HLS interval maps to exactly one fragment.
	mp := mustMPD(t, `<MPD type="static" mediaPresentationDuration="PT8S">
 <Period id="p0" start="PT0S" duration="PT8S">
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <SegmentTemplate timescale="90000" duration="360000" startNumber="1" media="v1080/$Number$.m4s"/>
   <Representation id="v1080" bandwidth="5000000"/>
  </AdaptationSet>
 </Period>
</MPD>`)
	pl := mustPL(t, fourSecondPlaylist)
	scan := &segment.Result{
		Path: "v1080.mp4",
		Segments: []segment.Boundary{
			{Index: 0, Offset: 100, Size: 200, SampleCount: 3},
			{Index: 1, Offset: 300, Size: 200, SampleCount: 3, Corrupt: true, ErrorCode: string(model.CodeTruncatedBox)},
		},
	}
	m := Build(Input{MPD: mp, Playlist: pl, Scans: []Scan{{RepID: "v1080", Path: "v1080.mp4", Result: scan}}})
	iv := m.Intervals[1]
	for _, e := range iv.Entries {
		if e.RepresentationID != "v1080" {
			continue
		}
		if !e.Corrupt || e.CorruptReason != string(model.CodeTruncatedBox) {
			t.Fatalf("corruption not propagated: %+v", e)
		}
		if e.MediaOffset != 300 || e.ScanSampleCount != 3 {
			t.Fatalf("scan boundary not attached: %+v", e)
		}
		if iv.Deliverable {
			t.Fatal("interval with corrupt entry reported deliverable")
		}
	}
}

func TestSegmentsAreTraceable(t *testing.T) {
	mp := mustMPD(t, genMPD("PT0S", "PT8S"))
	pl := mustPL(t, fourSecondPlaylist)
	m := Build(Input{
		MPD: mp, Playlist: pl,
		Inputs: []InputRef{
			{Role: "mpd", Path: "fixture.mpd", SHA256: strings.Repeat("a", 64)},
			{Role: "hls", Path: "fixture.m3u8", SHA256: strings.Repeat("b", 64)},
			{Role: "media", Representation: "v1080", Path: "v1080.mp4", SHA256: strings.Repeat("c", 64)},
		},
	})
	for _, iv := range m.Intervals {
		for _, e := range iv.Entries {
			if e.MPDDigest != strings.Repeat("a", 64) {
				t.Fatalf("entry %s lacks the MPD digest", e.RepresentationID)
			}
			if e.RepresentationID == "v1080" && e.MediaDigest != strings.Repeat("c", 64) {
				t.Fatalf("v1080 entry lacks the media digest")
			}
		}
	}
}
