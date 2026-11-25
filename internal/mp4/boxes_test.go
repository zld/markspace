package mp4

import (
	"encoding/binary"
	"errors"
	"testing"

	"mediad/internal/timeline"
)

func box(typ string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf[:4], size)
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}

func largeBox(typ string, payload []byte) []byte {
	buf := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(buf[:4], 1)
	copy(buf[4:8], typ)
	binary.BigEndian.PutUint64(buf[8:16], uint64(16+len(payload)))
	copy(buf[16:], payload)
	return buf
}

func expectMedia(t *testing.T, err error, code string) *timeline.MediaError {
	t.Helper()
	var me *timeline.MediaError
	if !errors.As(err, &me) {
		t.Fatalf("expected MediaError, got %v", err)
	}
	if me.Code != code {
		t.Fatalf("expected code %s, got %s (%v)", code, me.Code, err)
	}
	return me
}

func TestScanTopLevelOK(t *testing.T) {
	moof := box("moof", append(box("mfhd", []byte{0, 0, 0, 0, 0, 0, 0, 1}),
		box("traf", box("tfhd", []byte{0, 0, 0, 0, 0, 0, 0, 1}))...))
	mdat := box("mdat", make([]byte, 10))
	data := append(moof, mdat...)
	boxes, err := ScanTopLevel(NewSeeker(data), "f.m4s", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 || boxes[0].Type != "moof" || boxes[1].Type != "mdat" {
		t.Fatalf("unexpected boxes: %+v", boxes)
	}
	mfhd := boxes[0].FindPath("traf", "tfhd")
	if mfhd == nil || mfhd.Type != "tfhd" {
		t.Fatalf("nested tfhd not found: %+v", boxes[0].Children)
	}
	if got := boxes[1].PayloadLen; got != 10 {
		t.Fatalf("mdat payload len = %d, want 10", got)
	}
}

func TestScanTruncatedHeader(t *testing.T) {
	data := box("moof", box("mfhd", nil))[:5] // only 5 of 8 header bytes
	_, err := ScanTopLevel(NewSeeker(data), "cut.m4s", Limits{})
	me := expectMedia(t, err, timeline.ErrBoxTruncated)
	if me.Offset != 0 {
		t.Fatalf("offset = %d, want 0", me.Offset)
	}
}

func TestScanIllegalLength(t *testing.T) {
	data := make([]byte, 8)
	binary.BigEndian.PutUint32(data[:4], 4)
	copy(data[4:8], "free")
	_, err := ScanTopLevel(NewSeeker(data), "bad.m4s", Limits{})
	me := expectMedia(t, err, timeline.ErrBadBoxLength)
	if me.Offset != 0 {
		t.Fatalf("offset = %d, want 0", me.Offset)
	}
}

func TestScanMdatTruncatedAtExactCut(t *testing.T) {
	mdat := box("mdat", make([]byte, 100))
	data := mdat[:40] // physical cut at byte 40
	_, err := ScanTopLevel(NewSeeker(data), "cut-mdat.m4s", Limits{})
	me := expectMedia(t, err, timeline.ErrBoxTruncated)
	if me.Offset != 40 {
		t.Fatalf("truncation offset = %d, want 40 (±1 tolerance required)", me.Offset)
	}
}

func TestScanNestedTruncation(t *testing.T) {
	inner := box("mfhd", nil)[:6] // child header cut
	moof := box("moof", inner)
	_, err := ScanTopLevel(NewSeeker(moof), "nested.m4s", Limits{})
	me := expectMedia(t, err, timeline.ErrBoxTruncated)
	if me.Offset != 8 {
		t.Fatalf("nested cut offset = %d, want 8", me.Offset)
	}
}

func TestScanLimits(t *testing.T) {
	data := box("mdat", make([]byte, 64))
	_, err := ScanTopLevel(NewSeeker(data), "big.m4s", Limits{MaxBoxSize: 32})
	expectMedia(t, err, timeline.ErrLimitExceeded)

	_, err = ScanTopLevel(NewSeeker(data), "big.m4s", Limits{MaxFileSize: 16})
	expectMedia(t, err, timeline.ErrLimitExceeded)
}

func TestScanLargeSizeBox(t *testing.T) {
	data := largeBox("mdat", make([]byte, 20))
	boxes, err := ScanTopLevel(NewSeeker(data), "large.m4s", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if boxes[0].HeaderSize != 16 || boxes[0].PayloadLen != 20 {
		t.Fatalf("largesize box mis-parsed: %+v", boxes[0])
	}
	bad := largeBox("mdat", nil)
	binary.BigEndian.PutUint64(bad[8:16], 10)
	_, err = ScanTopLevel(NewSeeker(bad), "bad-large.m4s", Limits{})
	expectMedia(t, err, timeline.ErrBadBoxLength)
}
