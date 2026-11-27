package ebml

import "testing"

func TestAsBoxInfoKnownLength(t *testing.T) {
	el := &Element{Offset: 10, HeaderLen: 5, Size: 20, DataOffset: 15}
	bi := AsBoxInfo(el)
	if bi.Offset != 10 || bi.HeaderSize != 5 || bi.Size != 25 || bi.ExtendToEOF {
		t.Fatalf("unexpected BoxInfo: %+v", bi)
	}
	if start, end := PayloadRange(el, 999); start != 15 || end != 35 {
		t.Fatalf("payload range = (%d,%d)", start, end)
	}
}

func TestAsBoxInfoUnknownLength(t *testing.T) {
	el := &Element{Offset: 10, HeaderLen: 2, Size: -1, DataOffset: 12}
	bi := AsBoxInfo(el)
	if !bi.ExtendToEOF {
		t.Fatal("unknown-length element must map to ExtendToEOF")
	}
	if start, end := PayloadRange(el, 50); start != 12 || end != 50 {
		t.Fatalf("unknown payload range = (%d,%d), want (12,50)", start, end)
	}
}
