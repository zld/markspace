package timeline

import (
	"encoding/binary"
	"strings"
	"testing"
)

func elstV0(entries ...[3]int32) []byte {
	b := make([]byte, 8, 8+12*len(entries))
	binary.BigEndian.PutUint32(b[4:8], uint32(len(entries)))
	for _, e := range entries {
		var en [12]byte
		binary.BigEndian.PutUint32(en[0:4], uint32(e[0]))
		binary.BigEndian.PutUint32(en[4:8], uint32(e[1]))
		binary.BigEndian.PutUint16(en[8:10], uint16(int16(e[2])))
		b = append(b, en[:]...)
	}
	return b
}

func TestParseElstEmptyAndMediaEdits(t *testing.T) {
	edits, err := ParseElst(elstV0([3]int32{5000, -1, 1}, [3]int32{15000, 0, 1}))
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 || edits[0].MediaTime != -1 || edits[1].MediaTime != 0 {
		t.Fatalf("got %+v", edits)
	}
}

func TestParseElstRejectsNegativeMediaTime(t *testing.T) {
	_, err := ParseElst(elstV0([3]int32{1000, -2, 1}))
	if err == nil || !strings.Contains(err.Error(), "negative media time") {
		t.Fatalf("got %v", err)
	}
}

func TestParseElstRejectsNonUnitRate(t *testing.T) {
	e := elstV0([3]int32{1000, 0, 1})
	binary.BigEndian.PutUint16(e[18:20], 32768) // rate 1.5
	if _, err := ParseElst(e); err == nil || !strings.Contains(err.Error(), "unsupported rate") {
		t.Fatalf("got %v", err)
	}
}

func TestParseElstVersion1(t *testing.T) {
	b := make([]byte, 8+20)
	b[0] = 1
	binary.BigEndian.PutUint32(b[4:8], 1)
	binary.BigEndian.PutUint64(b[8:16], 4000)
	binary.BigEndian.PutUint64(b[16:24], 90000)
	binary.BigEndian.PutUint16(b[24:26], 1)
	edits, err := ParseElst(b)
	if err != nil {
		t.Fatal(err)
	}
	if edits[0].SegmentDuration != 4000 || edits[0].MediaTime != 90000 {
		t.Fatalf("got %+v", edits[0])
	}
}

func TestMapMovieTime(t *testing.T) {
	edits, _ := ParseElst(elstV0([3]int32{5000, -1, 1}, [3]int32{15000, 0, 1}))
	// Inside the empty edit: hole.
	if _, st := MapMovieTime(edits, 2500, 1000, 90000); st != MapHole {
		t.Fatalf("want hole, got %v", st)
	}
	// 12.5 s movie -> 7.5 s media = 675000 ticks.
	media, st := MapMovieTime(edits, 12500, 1000, 90000)
	if st != MapOK || media != 675000 {
		t.Fatalf("got media=%d status=%v", media, st)
	}
	// Past the last segment.
	if _, st := MapMovieTime(edits, 25000, 1000, 90000); st != MapBeyondEnd {
		t.Fatalf("want beyond-end, got %v", st)
	}
}

func TestUnwrap32NoWrap(t *testing.T) {
	out, wraps, err := Unwrap32([]uint32{10, 20, 30})
	if err != nil || wraps != 0 {
		t.Fatalf("got %v wraps=%d err=%v", out, wraps, err)
	}
	if out[2] != 30 {
		t.Fatalf("got %v", out)
	}
}

func TestUnwrap32GenuineWrap(t *testing.T) {
	out, wraps, err := Unwrap32([]uint32{0xFFFFFFF0, 0x00000010, 0x00000020})
	if err != nil {
		t.Fatal(err)
	}
	if wraps != 1 {
		t.Fatalf("want 1 wrap, got %d", wraps)
	}
	want := int64(0xFFFFFFF0) + 0x20
	if out[1] != want || out[2] != want+0x10 {
		t.Fatalf("got %v", out)
	}
}

func TestUnwrap32BackwardIsCorruption(t *testing.T) {
	_, _, err := Unwrap32([]uint32{1000, 500})
	if err == nil || !strings.Contains(err.Error(), "moved backward") {
		t.Fatalf("got %v", err)
	}
}
