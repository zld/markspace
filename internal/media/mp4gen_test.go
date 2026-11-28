package media

import (
	"bytes"
	"testing"

	gomp4 "github.com/abema/go-mp4"
)

func basicFile(t *testing.T) FileSpec {
	t.Helper()
	return FileSpec{Asset: "clip", Tracks: []TrackSpec{
		{ID: 1, Handler: "vide", Timescale: 90_000, EditMedia: 4_500, EditDur: 90_000,
			EmptyEditTicks: 2_250, StartTicks: 4_500,
			Samples: SampleSpec{Count: 8, Size: 120, Delta: 4_500, KeyEvery: 4}},
		{ID: 2, Handler: "soun", Timescale: 48_000,
			Samples: SampleSpec{Count: 16, Size: 40, Delta: 2_400}},
	}}
}

func TestGoMP4DecodesGeneratedFile(t *testing.T) {
	data, err := BuildFile(basicFile(t))
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)

	moovs, err := gomp4.ExtractBox(r, nil, gomp4.BoxPath{gomp4.BoxTypeMoov()})
	if err != nil {
		t.Fatalf("go-mp4 cannot walk the file: %v", err)
	}
	if len(moovs) != 1 {
		t.Fatalf("moov boxes = %d", len(moovs))
	}

	traks, err := gomp4.ExtractBoxesWithPayload(r, nil, []gomp4.BoxPath{
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeTkhd()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMdhd()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeHdlr()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeEdts(), gomp4.BoxTypeElst()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(),
			gomp4.BoxTypeStbl(), gomp4.BoxTypeStsz()},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tkhds, mdhds, elsts, stszs int
	var editEntries int
	for _, b := range traks {
		switch p := b.Payload.(type) {
		case *gomp4.Tkhd:
			tkhds++
			if p.TrackID == 0 {
				t.Fatal("track id not encoded")
			}
		case *gomp4.Mdhd:
			mdhds++
			if p.Timescale == 0 {
				t.Fatal("timescale not encoded")
			}
		case *gomp4.Elst:
			elsts++
			editEntries += len(p.Entries)
		case *gomp4.Stsz:
			stszs++
			if p.SampleCount == 0 {
				t.Fatal("sample count not encoded")
			}
		}
	}
	if tkhds != 2 || mdhds != 2 || elsts != 2 || stszs != 2 {
		t.Fatalf("tkhd=%d mdhd=%d elst=%d stsz=%d", tkhds, mdhds, elsts, stszs)
	}
	// The video track has an empty edit plus a media edit.
	if editEntries < 3 {
		t.Fatalf("edit entries = %d, want >= 3", editEntries)
	}
}

func TestGoMP4ReadsEditListMediaTime(t *testing.T) {
	data, err := BuildFile(basicFile(t))
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := gomp4.ExtractBoxWithPayload(bytes.NewReader(data), nil, gomp4.BoxPath{
		gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeEdts(), gomp4.BoxTypeElst(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) == 0 {
		t.Fatal("no elst found")
	}
	elst := boxes[0].Payload.(*gomp4.Elst)
	if elst.GetMediaTime(0) != -1 {
		t.Fatalf("first edit should be empty, media time = %d", elst.GetMediaTime(0))
	}
	if elst.GetMediaTime(1) != 4_500 {
		t.Fatalf("media edit time = %d", elst.GetMediaTime(1))
	}
	if elst.GetSegmentDuration(1) != 90_000 {
		t.Fatalf("media edit duration = %d", elst.GetSegmentDuration(1))
	}
}

func TestGoMP4RejectsTruncatedFile(t *testing.T) {
	data, err := BuildFile(basicFile(t))
	if err != nil {
		t.Fatal(err)
	}
	// Cut inside moov: the truncated box must fail to parse. (Cutting inside
	// mdat would look like a smaller mdat to a box walker, which is exactly
	// why the bounded scanner validates box ends itself.)
	moovs, err := gomp4.ExtractBox(bytes.NewReader(data), nil, gomp4.BoxPath{gomp4.BoxTypeMoov()})
	if err != nil || len(moovs) != 1 {
		t.Fatalf("locate moov: %v", err)
	}
	cut := int(moovs[0].Offset) + int(moovs[0].Size)/2
	// A deep walk must fail: moov's children claim more bytes than remain.
	_, err = gomp4.ExtractBoxesWithPayload(bytes.NewReader(data[:cut]), nil, []gomp4.BoxPath{
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMdhd()},
	})
	if err == nil {
		t.Fatal("go-mp4 decoded a moov whose children were truncated")
	}
}

func TestStripTableReachesNestedBoxes(t *testing.T) {
	data, err := BuildFile(basicFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if n := StripTable(data, "stco"); n != 2 {
		t.Fatalf("stripped %d stco boxes, want 2", n)
	}
	// The container must still parse; only the table bytes are gone.
	if _, err := gomp4.ExtractBox(bytes.NewReader(data), nil,
		gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak()}); err != nil {
		t.Fatalf("container damaged by strip: %v", err)
	}
}
