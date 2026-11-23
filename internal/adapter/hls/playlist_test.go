package hls

import (
	"bytes"
	"testing"

	"mediaqc/internal/adapter"
	"mediaqc/internal/model"
)

func parser() *Parser { return NewParser(adapter.DefaultLimits()) }

const validPlaylist = `#EXTM3U
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:100
#EXTINF:4.0,
seg1.ts
#EXT-X-DISCONTINUITY
#EXTINF:3.5,
seg2.ts
#EXTINF:4.0,
missing:seg3.ts
`

func TestParseValidPlaylist(t *testing.T) {
	pl, err := parser().ParseBytes("mat-hls-1", []byte(validPlaylist))
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Segments) != 3 || pl.MediaSequence != 100 {
		t.Fatalf("segments=%d seq=%d", len(pl.Segments), pl.MediaSequence)
	}
	if !pl.Segments[1].Discontinuity || len(pl.Discontinuities) != 1 {
		t.Fatal("discontinuity marker not captured")
	}
	if !pl.Segments[2].Missing {
		t.Fatal("missing marker not captured")
	}
	events, errs := pl.Events("mat-hls-1", "v", model.TrackVideo)
	if len(events) != 3 || len(errs) != 1 {
		t.Fatalf("events=%d errs=%d", len(events), len(errs))
	}
	if !model.HasCode(errs[0], model.CodeMissingReference) || !model.IsRecoverable(errs[0]) {
		t.Fatal("missing reference must be a recoverable model error")
	}
	// monotonic starts: 0, 4000, 7500 ms
	if events[2].Start.Ticks() != 7500 {
		t.Fatalf("third start = %d, want 7500", events[2].Start.Ticks())
	}
}

func TestRejectsDuplicateSequence(t *testing.T) {
	src := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:2.0,
a.ts
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:2.0,
b.ts
`
	_, err := parser().ParseBytes("mat-dup", []byte(src))
	if err == nil || !model.HasCode(err, model.CodeDuplicateSeq) {
		t.Fatalf("want duplicate-sequence error, got %v", err)
	}
}

func TestRejectsUnsupportedTagAndBadHeader(t *testing.T) {
	bad := []string{
		"NOTM3U\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXTINF:1,\n",
	}
	for _, b := range bad {
		if _, err := parser().ParseBytes("mat-bad", []byte(b)); err == nil {
			t.Fatalf("expected rejection for %q", b)
		}
	}
}

func TestRejectsOversizedPlaylist(t *testing.T) {
	lim := adapter.DefaultLimits()
	lim.MaxBytes = 40
	p := NewParser(lim)
	_, err := p.Parse("mat-big", bytes.NewReader([]byte(validPlaylist)), int64(len(validPlaylist)))
	if err == nil || !model.HasCode(err, model.CodeConfig) {
		t.Fatalf("want config/size error, got %v", err)
	}
}

func TestRegistryRegistration(t *testing.T) {
	got, err := adapter.Default.Lookup(adapter.KindHLS, "m3u8-local", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Supports("discontinuity") {
		t.Fatal("registered parser must advertise discontinuity")
	}
}
