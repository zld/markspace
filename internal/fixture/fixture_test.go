package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"
)

func TestInitAndSegmentParse(t *testing.T) {
	dir := t.TempDir()
	track := TrackSpec{ID: 1, Timescale: 90000, Video: true, Width: 640, Height: 360}
	initData := buildInit(track)
	if _, err := writeFile(dir, "init.mp4", initData); err != nil {
		t.Fatal(err)
	}
	segs := []SampleSpec{{DurTicks: 3000, Size: 200, Key: true}, {DurTicks: 3000, Size: 180}, {DurTicks: 3000, Size: 190}}
	segData := buildSegment(track, 1, 0, segs, 23)
	if _, err := writeFile(dir, "seg-1.m4s", segData); err != nil {
		t.Fatal(err)
	}

	// Init: timescale and track id.
	f, _ := os.Open(filepath.Join(dir, "init.mp4"))
	defer f.Close()
	bips, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1(), mp4.BoxTypeAvcC()},
	})
	if err != nil {
		t.Fatalf("init parse: %v", err)
	}
	var sawTS, sawTrack, sawAvcC bool
	for _, b := range bips {
		switch p := b.Payload.(type) {
		case *mp4.Tkhd:
			sawTrack = p.TrackID == 1
		case *mp4.Mdhd:
			sawTS = p.Timescale == 90000
		case *mp4.AVCDecoderConfiguration:
			sawAvcC = p.LengthSizeMinusOne == 3
		}
	}
	if !sawTS || !sawTrack || !sawAvcC {
		t.Fatalf("init missing boxes ts=%v track=%v avcC=%v (%d found)", sawTS, sawTrack, sawAvcC, len(bips))
	}

	// Segment: moof with one trun of 3 samples.
	sf, _ := os.Open(filepath.Join(dir, "seg-1.m4s"))
	defer sf.Close()
	var moof *mp4.BoxInfo
	for {
		bi, err := mp4.ReadBoxInfo(sf)
		if err != nil {
			break
		}
		if bi.Type == mp4.BoxTypeMoof() {
			moof = bi
			break
		}
		if _, err := bi.SeekToEnd(sf); err != nil {
			t.Fatal(err)
		}
	}
	if moof == nil {
		t.Fatal("no moof")
	}
	tb, err := mp4.ExtractBoxesWithPayload(sf, moof, []mp4.BoxPath{
		{mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
	})
	if err != nil {
		t.Fatalf("seg parse: %v", err)
	}
	var trun *mp4.Trun
	for _, b := range tb {
		switch p := b.Payload.(type) {
		case *mp4.Tfdt:
			if p.GetBaseMediaDecodeTime() != 0 {
				t.Errorf("base decode time = %d", p.GetBaseMediaDecodeTime())
			}
		case *mp4.Trun:
			trun = p
		}
	}
	if trun == nil || trun.SampleCount != 3 {
		t.Fatalf("trun = %+v", trun)
	}
	if trun.Entries[0].SampleFlags&0x00010000 != 0 {
		t.Error("first sample should be sync")
	}
	if trun.Entries[1].SampleFlags&0x00010000 == 0 {
		t.Error("second sample should be non-sync")
	}
}
