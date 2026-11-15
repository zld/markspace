package cmaf

import (
	"encoding/binary"
	"errors"
	"testing"
)

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], []byte(typ))
	copy(out[8:], payload)
	return out
}

func TestScanBoxesRoundTrip(t *testing.T) {
	data := append(box("moof", []byte("abc")), box("mdat", []byte("defg"))...)
	boxes, err := ScanBoxes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 || boxes[0].Type != "moof" || string(boxes[0].Data) != "abc" {
		t.Fatalf("unexpected boxes: %+v", boxes)
	}
	if boxes[0].End() != 11 || boxes[1].Start != 11 || boxes[1].End() != int64(len(data)) {
		t.Fatalf("offset bookkeeping wrong: %+v", boxes)
	}
}

func TestScanRejectsTruncated(t *testing.T) {
	if _, err := ScanBoxes([]byte{0, 0, 0, 20, 'm', 'd', 'a', 't'}); !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("want ErrOutOfBounds, got %v", err)
	}
	if _, err := ScanBoxes([]byte{0, 0, 0, 4, 'm', 'd'}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	if _, err := ScanBoxes([]byte{0, 0, 0, 3, 'm', 'd', 'a', 't'}); err == nil {
		t.Fatal("box smaller than header must fail")
	}
}

func TestScanRejectsOversized(t *testing.T) {
	data := make([]byte, 8)
	binary.BigEndian.PutUint32(data[0:4], uint32(MaxBoxSize+1))
	copy(data[4:8], "mdat")
	if _, err := ScanBoxes(data); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestLargeSizeBox(t *testing.T) {
	payload := []byte("hello")
	data := make([]byte, largeHeaderSize+len(payload))
	binary.BigEndian.PutUint32(data[0:4], 1)
	copy(data[4:8], "mdat")
	binary.BigEndian.PutUint64(data[8:16], uint64(largeHeaderSize+len(payload)))
	copy(data[16:], payload)
	boxes, err := ScanBoxes(data)
	if err != nil {
		t.Fatal(err)
	}
	if boxes[0].Header != largeHeaderSize || string(boxes[0].Data) != "hello" {
		t.Fatalf("largesize parse failed: %+v", boxes[0])
	}
}

func TestLargeSizeOverflow(t *testing.T) {
	data := make([]byte, largeHeaderSize)
	binary.BigEndian.PutUint32(data[0:4], 1)
	copy(data[4:8], "mdat")
	binary.BigEndian.PutUint64(data[8:16], 1<<63)
	if _, err := ScanBoxes(data); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestFindPathAndReaders(t *testing.T) {
	inner := box("tfhd", []byte{0, 0, 0, 0, 0x12, 0x34})
	traf := box("traf", inner)
	moofBox := box("moof", traf)
	root, err := ScanBoxes(moofBox)
	if err != nil {
		t.Fatal(err)
	}
	b, err := FindPath(root[0].Data, "traf", "tfhd")
	if err != nil {
		t.Fatal(err)
	}
	ver, flags, payload, err := b.FullHeader()
	if err != nil || ver != 0 || flags != 0 || len(payload) != 2 {
		t.Fatalf("full header parse failed: %v %d %d %d", err, ver, flags, len(payload))
	}
	v, err := U16(payload, 0)
	if err != nil || v != 0x1234 {
		t.Fatalf("U16 failed: %v %d", err, v)
	}
	if _, err := U32(payload, 0); !errors.Is(err, ErrTruncated) {
		t.Fatalf("want truncated U32, got %v", err)
	}
	if _, err := FindPath(root[0].Data, "traf", "mfhd"); err == nil {
		t.Fatal("missing nested box must error")
	}
}
