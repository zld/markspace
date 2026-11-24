package demux_test

import (
	"encoding/binary"
	"testing"

	"mediarc/internal/demux"
	"mediarc/internal/fixture"
	"mediarc/internal/model"
)

func TestEncodeDemuxSyntheticArchive(t *testing.T) {
	a := fixture.GenerateArchive(fixture.DefaultOptions())
	raw, err := demux.EncodeContainer(a)
	if err != nil {
		t.Fatal(err)
	}
	c, err := demux.Demux(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Archive == nil || len(c.Archive.Tracks) != 5 {
		t.Fatalf("unexpected archive: %+v", c.Archive)
	}
	if len(c.Archive.Samples) != 78 {
		t.Fatalf("samples = %d, want 78", len(c.Archive.Samples))
	}
	if len(c.Boxes) < 4 || c.Boxes[0].Type != "ftyp" {
		t.Fatalf("boxes not scanned: %+v", c.Boxes)
	}
	if c.GoMP4TrackCount != 5 {
		t.Fatalf("go-mp4 probe tracks = %d, want 5", c.GoMP4TrackCount)
	}
	var sawParsed bool
	for _, ev := range c.Archive.Events {
		if ev.Text == "" && len(ev.Fields) == 0 {
			t.Fatalf("metadata event %s was not parsed", ev.ID)
		}
		if ev.Kind == model.MetaEmsg && ev.Fields["scheme_id_uri"] == "" {
			t.Fatalf("emsg fields missing: %+v", ev.Fields)
		}
		sawParsed = true
	}
	if !sawParsed {
		t.Fatal("no metadata events parsed")
	}
}

func TestDemuxTruncatedReportsOffset(t *testing.T) {
	a := fixture.GenerateArchive(fixture.DefaultOptions())
	raw, err := demux.EncodeContainer(a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = demux.Demux(demux.Truncate(raw, 160))
	if !model.HasCode(err, model.CodeTruncated) {
		t.Fatalf("want truncated code, got %v", err)
	}
	me, _ := model.AsMediaError(err)
	if me.SourceOffset == nil || *me.SourceOffset != 160 {
		t.Fatalf("offset = %v, want 160: %v", me.SourceOffset, err)
	}
}

func TestDemuxUnknownBoxReportsOffset(t *testing.T) {
	a := fixture.GenerateArchive(fixture.DefaultOptions())
	raw, err := demux.EncodeContainer(a)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("critical-unknown")
	box := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(box[:4], uint32(len(box)))
	copy(box[4:8], "XXXX")
	copy(box[8:], payload)
	unknownOffset := int64(len(raw))
	raw = append(raw, box...)

	_, err = demux.Demux(raw)
	if !model.HasCode(err, model.CodeUnsupported) {
		t.Fatalf("want unsupported structure, got %v", err)
	}
	me, _ := model.AsMediaError(err)
	if me.SourceOffset == nil || *me.SourceOffset != unknownOffset {
		t.Fatalf("offset = %v, want %d", me.SourceOffset, unknownOffset)
	}
}

func TestMOVBrandRoundTrip(t *testing.T) {
	opt := fixture.DefaultOptions()
	opt.BrandMOV = true
	raw, err := demux.EncodeContainer(fixture.GenerateArchive(opt))
	if err != nil {
		t.Fatal(err)
	}
	c, err := demux.Demux(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Brand != "qt  " {
		t.Fatalf("brand = %q, want QuickTime qt", c.Brand)
	}
}
