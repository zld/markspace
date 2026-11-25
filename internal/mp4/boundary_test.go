package mp4

import (
	"testing"
)

func TestBuildAndAnalyzeFragmentBoundary(t *testing.T) {
	payload := []byte("deterministic-game-frame-payload")
	data, err := BuildFragment(7, 1_800_000, payload)
	if err != nil {
		t.Fatal(err)
	}
	info, err := AnalyzeFragment("seg-7.m4s", data)
	if err != nil {
		t.Fatal(err)
	}
	if info.DecodeTime != 1_800_000 {
		t.Fatalf("decode time=%d", info.DecodeTime)
	}
	if int(info.MdatOffset+info.MdatSize) != len(data) {
		t.Fatal("mdat must end exactly at EOF")
	}
	if info.MdatPayloadOff >= uint64(len(data)) {
		t.Fatal("payload offset outside fragment")
	}
	found := false
	for _, b := range info.Boxes {
		if b.Type == "moof" || b.Type == "mdat" || b.Type == "styp" {
			found = true
		}
	}
	if !found || len(info.Boxes) != 3 {
		t.Fatalf("boxes=%+v", info.Boxes)
	}
}

func TestCorruptDecodeTimeIsParsed(t *testing.T) {
	data, err := BuildFragment(1, 100, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := CorruptDecodeTime(data, 900); err != nil {
		t.Fatal(err)
	}
	info, err := AnalyzeFragment("bad.m4s", data)
	if err != nil {
		t.Fatal(err)
	}
	if info.DecodeTime != 900 {
		t.Fatalf("corrupted decode time=%d want 900", info.DecodeTime)
	}
}

func TestTruncatedFragmentRejected(t *testing.T) {
	data, err := BuildFragment(1, 100, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeFragment("truncated.m4s", data[:len(data)-3]); err == nil {
		t.Fatal("truncated MP4 unexpectedly accepted")
	}
}
