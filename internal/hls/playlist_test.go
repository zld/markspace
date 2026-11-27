package hls

import (
	"testing"

	"mediarc/internal/model"
)

const mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.0,
seg0.ts
#EXTINF:2.0,
seg1.ts
#EXT-X-DISCONTINUITY
#EXTINF:2.0,
seg2.ts
#EXTINF:2.0,
seg3.ts
#EXT-X-ENDLIST
`

func TestParseMedia(t *testing.T) {
	pl, err := ParseMedia("v.m3u8", []byte(mediaPlaylist))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pl.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", pl.Errors)
	}
	if len(pl.Entries) != 4 {
		t.Fatalf("entries=%d want 4", len(pl.Entries))
	}
	wantStart := []int64{0, 2000, 4000, 6000}
	for i, e := range pl.Entries {
		if e.Segment.StartMs != wantStart[i] || e.Segment.DurationMs != 2000 {
			t.Fatalf("entry %d timing wrong: %+v", i, e)
		}
	}
	disc := pl.DiscontinuityIndexes()
	if len(disc) != 1 || disc[0] != 2 {
		t.Fatalf("discontinuity indexes=%v want [2]", disc)
	}
	if pl.TotalMs != 8000 || pl.MediaSequence != 0 {
		t.Fatalf("totals wrong: %+v", pl)
	}
}

func TestParseMediaDuplicateAndMissingDuration(t *testing.T) {
	src := `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXTINF:2.0,
seg0.ts
seg1.ts
#EXTINF:2.0,
seg0.ts
`
	pl, err := ParseMedia("bad.m3u8", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasKind(pl.Errors, model.KindDuplicateReference) {
		t.Fatalf("duplicate not detected: %v", pl.Errors)
	}
	if !hasKind(pl.Errors, model.KindMissingDuration) {
		t.Fatalf("missing EXTINF not detected: %v", pl.Errors)
	}
	for _, e := range pl.Errors {
		if e.Line == 0 {
			t.Fatalf("errors must carry line numbers: %v", e)
		}
	}
}

func TestParseMediaUnparsableTagAndInf(t *testing.T) {
	src := `#EXTM3U
#EXT-X-MYSTERIOUS-TAG:xx
#EXTINF:not-a-number,
seg0.ts
`
	pl, err := ParseMedia("u.m3u8", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasKind(pl.Errors, model.KindManifestSyntax) || !hasKind(pl.Errors, model.KindMissingDuration) {
		t.Fatalf("tag/inf errors missing: %v", pl.Errors)
	}
}

func TestParseMediaTruncated(t *testing.T) {
	if _, err := ParseMedia("t.m3u8", []byte("#EXTINF:2.0,\nseg0.ts\n")); err == nil {
		t.Fatal("missing #EXTM3U must be a truncated/syntax error")
	}
}

func TestParseMediaRangeOutOfBounds(t *testing.T) {
	// Target duration 1 s but segments declare 5 s each.
	src := `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXTINF:5.0,
seg0.ts
#EXTINF:5.0,
seg1.ts
`
	pl, err := ParseMedia("r.m3u8", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasKind(pl.Errors, model.KindRangeOutOfBounds) {
		t.Fatalf("range check missing: %v", pl.Errors)
	}
}

func TestParseMaster(t *testing.T) {
	src := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=400000,RESOLUTION=640x360
low.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1200000
high.m3u8
`
	v, errs, err := ParseMaster("master.m3u8", []byte(src))
	if err != nil || len(errs) != 0 {
		t.Fatalf("parse: err=%v errs=%v", err, errs)
	}
	if len(v) != 2 || v[0].Bandwidth != 400000 || v[1].URL != "high.m3u8" {
		t.Fatalf("variants wrong: %+v", v)
	}
}

func hasKind(errs []*model.MediaError, kind model.ErrorKind) bool {
	for _, e := range errs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
