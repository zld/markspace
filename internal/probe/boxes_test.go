package probe

import (
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/fixture"
)

func demoFile() *fixture.BuiltFile {
	return fixture.BuildFile(fixture.MediaSpec{
		Brand: "isom",
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Timescale: 48000, SampleCount: 8, SampleDelta: 480,
				PayloadSize: 8, SyncInterval: 4, HandlerType: "soun"},
			{TrackID: 2, Timescale: 24000, SampleCount: 8, SampleDelta: 240,
				PayloadSize: 8, SyncInterval: 4, HandlerType: "vide"},
		},
	})
}

func TestScanFindsNestedBoxes(t *testing.T) {
	f := demoFile()
	nodes, faults := Scan(f.Data)
	if len(faults) != 0 {
		t.Fatalf("clean file produced faults: %v", faults)
	}
	for _, want := range []string{"ftyp", "moov", "mdat"} {
		if Find(nodes, want) == nil {
			t.Fatalf("top-level box %s missing", want)
		}
	}
	stss := FindAll(nodes, "stss")
	if len(stss) != 2 {
		t.Fatalf("stss count = %d, want 2", len(stss))
	}
	if got := FindAll(nodes, "stco"); len(got) != 2 {
		t.Fatalf("stco count = %d, want 2", len(got))
	}
}

func TestScanFlagsTruncatedBoxAtOriginalOffset(t *testing.T) {
	f := demoFile()
	// Cut inside mdat so its header still promises the full size.
	cut := f.MdatData + 10
	trunc := f.Truncate(cut)
	_, faults := Scan(trunc)
	if len(faults) == 0 {
		t.Fatal("expected a truncation fault")
	}
	var saw bool
	for _, fe := range faults {
		if fe.Category == clock.CategoryTruncated && int64(fe.Offset) == int64(f.MdatOffset) {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("no truncated fault at mdat offset %d: %v", f.MdatOffset, faults)
	}
}

func TestScanFlagsOutOfBoundsSize(t *testing.T) {
	f := demoFile()
	bad := f.CorruptBoxSize(f.MdatOffset)
	_, faults := Scan(bad)
	var saw bool
	for _, fe := range faults {
		if fe.Category == clock.CategoryTruncated && int64(fe.Offset) == int64(f.MdatOffset) {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("oversized mdat not reported at %d: %v", f.MdatOffset, faults)
	}
}

func TestScanSiblingBoxesSurviveChildFault(t *testing.T) {
	f := demoFile()
	// Damage a nested stbl box size; the top-level ftyp must still parse.
	off := f.FindBoxHeader("stts")
	bad := f.CorruptBoxSize(off)
	nodes, faults := Scan(bad)
	if Find(nodes, "ftyp") == nil {
		t.Fatal("ftyp lost after nested fault")
	}
	if len(faults) == 0 {
		t.Fatal("expected fault from damaged nested box")
	}
}

func TestPayloadBoundsChecked(t *testing.T) {
	f := demoFile()
	nodes, _ := Scan(f.Data)
	mdat := Find(nodes, "mdat")
	p, err := mdat.Payload(f.Data)
	if err != nil || len(p) != int(mdat.PayloadLen) {
		t.Fatalf("payload read failed: %v len=%d", err, len(p))
	}
	if _, err := mdat.Payload(f.Data[:mdat.Offset+1]); err == nil {
		t.Fatal("expected bounds error on short input")
	}
}
