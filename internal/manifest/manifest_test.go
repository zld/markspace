package manifest

import (
	"strings"
	"testing"

	"archivecheck/internal/model"
)

const hlsSample = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:1.5,
seg/0.m4s
#EXTINF:2.25,title
seg/1.m4s
#EXT-X-DISCONTINUITY
#EXTINF:0.004,
seg/2.m4s
#EXTINF:0.004,
#EXT-X-BYTERANGE:1000@0
seg/3.m4s
#EXTINF:0.004,
#EXT-X-BYTERANGE:500
seg/3.m4s
#EXT-X-ENDLIST
`

func TestParseHLSTimelineAndTags(t *testing.T) {
	plan, err := ParseHLS("media.m3u8", strings.NewReader(hlsSample), Options{})
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if plan.Timebase != DefaultTimebase {
		t.Fatalf("timebase=%d", plan.Timebase)
	}
	if plan.MediaSequence != 7 || plan.TargetDuration != 4*DefaultTimebase {
		t.Fatalf("seq=%d target=%d", plan.MediaSequence, plan.TargetDuration)
	}
	if got := plan.SegmentCount(); got != 5 {
		t.Fatalf("segments=%d want 5", got)
	}
	rep := plan.Representations[0]
	wantStarts := []int64{0, 135000, 337500, 337860, 338220}
	wantDurs := []int64{135000, 202500, 360, 360, 360}
	for i, seg := range rep.Segments {
		if seg.Start != wantStarts[i] || seg.Duration != wantDurs[i] {
			t.Fatalf("segment %d start=%d dur=%d want %d/%d", i, seg.Start, seg.Duration, wantStarts[i], wantDurs[i])
		}
		if seg.ID != []string{"7", "8", "9", "10", "11"}[i] {
			t.Fatalf("segment %d id=%q", i, seg.ID)
		}
	}
	if !rep.Segments[2].Discontinuity {
		t.Fatal("segment 2 should carry the discontinuity flag")
	}
	if rep.Segments[0].Range != nil {
		t.Fatal("segment 0 has no range")
	}
	if r := rep.Segments[3].Range; r == nil || r.Start != 0 || r.End != 1000 {
		t.Fatalf("segment 3 range %+v", r)
	}
	if r := rep.Segments[4].Range; r == nil || r.Start != 1000 || r.End != 1500 {
		t.Fatalf("segment 4 continuation range %+v", r)
	}
	if plan.Duration != 338580 {
		t.Fatalf("duration=%d", plan.Duration)
	}
}

func TestParseHLSRejectsMissingDuration(t *testing.T) {
	src := "#EXTM3U\n#EXT-X-TARGETDURATION:2\nseg/0.m4s\n"
	_, err := ParseHLS("bad.m3u8", strings.NewReader(src), Options{})
	f, ok := model.AsFault(err)
	if !ok {
		t.Fatalf("want fault, got %v", err)
	}
	if f.Source.Line != 3 || f.Code != model.CodeManifestBoundary {
		t.Fatalf("fault %+v", f)
	}
	multi, ok := f.Cause.(*MultiError)
	if !ok || len(multi.Faults) == 0 {
		t.Fatalf("expected detailed faults, got %v", f.Cause)
	}
	var detail *model.Fault
	for _, f := range multi.Faults {
		if f.Code == model.CodeManifestBoundary && f.Source.Line == 3 {
			detail = f
		}
	}
	if detail == nil {
		t.Fatalf("no boundary fault at line 3 in %+v", multi.Faults)
	}
}

func TestParseHLSInexactDurationHasSourcePosition(t *testing.T) {
	src := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:0.033333333,\nseg/0.m4s\n"
	_, err := ParseHLS("inexact.m3u8", strings.NewReader(src), Options{})
	f, ok := model.AsFault(err)
	if !ok {
		t.Fatalf("want fault, got %v", err)
	}
	got := f
	if got.Code != model.CodeManifestTimebase || got.Source.Line != 3 {
		t.Fatalf("fault %+v", got)
	}
	if !strings.Contains(got.Message, "remainder") {
		t.Fatalf("message %q should mention the retained remainder", got.Message)
	}
	// The same playlist is accepted when the caller opts into carrying it.
	plan, err := ParseHLS("inexact.m3u8", strings.NewReader(src), Options{AllowInexact: true})
	if err != nil {
		t.Fatalf("allow-inexact: %v", err)
	}
	seg := plan.Representations[0].Segments[0]
	if seg.Duration != 2999 || seg.EndRemainder != (model.Rational{Num: 99997, Den: 100000}) {
		t.Fatalf("segment remainder %+v / %+v", seg, seg.EndRemainder)
	}
}

func TestParseHLSRejectsMasterAndRemote(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nv1.m3u8\n"
	if _, err := ParseHLS("master.m3u8", strings.NewReader(master), Options{}); err == nil {
		t.Fatal("master playlist must be rejected")
	}
	remote := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nhttps://example.com/a.m4s\n"
	if _, err := ParseHLS("remote.m3u8", strings.NewReader(remote), Options{}); err == nil {
		t.Fatal("remote URI must be rejected")
	}
}

func TestParseHLSBadByteRange(t *testing.T) {
	src := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\n#EXT-X-BYTERANGE:0@10\nseg/0.m4s\n"
	_, err := ParseHLS("range.m3u8", strings.NewReader(src), Options{})
	if err == nil {
		t.Fatal("zero-length byte range must be rejected")
	}
	if !strings.Contains(err.Error(), "manifest_bad_range") {
		t.Fatalf("err=%v", err)
	}
}

func TestAccumulatorKeepsSubTickRemainders(t *testing.T) {
	base := int64(720000)
	acc := NewAccumulator(base)
	d, err := model.SecondsToTime(1, 3, base)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for i := 0; i < 3; i++ {
		n, _, _, err := acc.Next(d)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != base {
		t.Fatalf("three thirds summed to %d ticks, want %d", total, base)
	}
	if !acc.Position().Rem.IsZero() {
		t.Fatalf("leftover %s", acc.Position().Rem)
	}
}

func TestParseDecimalSecondsExactness(t *testing.T) {
	r, err := ParseDecimalSeconds("6.006")
	if err != nil {
		t.Fatal(err)
	}
	if r != (model.Rational{Num: 3003, Den: 500}) {
		t.Fatalf("got %+v", r)
	}
	for _, bad := range []string{"", "-1", "1.1234567890", "1.2.3", "abc", "+1"} {
		if _, err := ParseDecimalSeconds(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}
