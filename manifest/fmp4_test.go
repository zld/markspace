package manifest

import (
	"encoding/binary"
	"testing"
)

func testBox(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func TestInspectFMP4AndTruncation(t *testing.T) {
	mfhd := make([]byte, 8) // version/flags + sequence number
	moof := testBox("moof", testBox("mfhd", mfhd))
	mdat := testBox("mdat", make([]byte, 32))
	good := append(append([]byte{}, moof...), mdat...)
	info, err := InspectFMP4(good, "seg.m4s")
	if err != nil {
		t.Fatalf("InspectFMP4: %v", err)
	}
	if !info.HasMoof || !info.HasMdat {
		t.Fatalf("unexpected info: %+v", info)
	}

	if _, err := InspectFMP4(good[:len(good)*2/3], "truncated.m4s"); err == nil {
		t.Fatal("expected truncation error")
	}
	if _, err := InspectFMP4([]byte("not an fmp4 fragment at all"), "corrupt.m4s"); err == nil {
		t.Fatal("expected corrupt fragment error")
	}
}
