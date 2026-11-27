package mp4

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"mediapipe/internal/model"
)

// rawBox assembles a box with an 8-byte header around payload.
func rawBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func largeBox(typ string, payload []byte) []byte {
	out := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(out[:4], 1)
	copy(out[4:8], typ)
	binary.BigEndian.PutUint64(out[8:16], uint64(len(out)))
	copy(out[16:], payload)
	return out
}

func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func walkStream(t *testing.T, data []byte, cfg Config) ([]Box, error) {
	t.Helper()
	s, err := NewStream(bytes.NewReader(data), int64(len(data)), "a.mp4", cfg)
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	var got []Box
	err = s.Walk(context.Background(), func(b Box) error {
		got = append(got, b)
		return nil
	})
	return got, err
}

func TestWalkRecordsOffsetsAndPaths(t *testing.T) {
	ftyp := rawBox("ftyp", []byte("isom0000isom"))
	inner := rawBox("stbl", rawBox("stts", make([]byte, 16)))
	moov := rawBox("moov", concat(rawBox("trak", rawBox("mdia", inner)), rawBox("mvhd", make([]byte, 100))))
	data := concat(ftyp, moov, rawBox("mdat", make([]byte, 64)))

	boxes, err := walkStream(t, data, Config{})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	want := map[string]int64{
		"ftyp": 0, "moov": 20, "trak": 28, "mdia": 36, "stbl": 44, "stts": 52, "mvhd": 76, "mdat": 184,
	}
	for _, b := range boxes {
		if off, ok := want[b.Type]; ok && b.Offset != off {
			t.Errorf("box %s offset = %d, want %d", b.Type, b.Offset, off)
		}
	}
	if len(boxes) != len(want) {
		t.Fatalf("walked %d boxes, want %d", len(boxes), len(want))
	}
	var stts Box
	for _, b := range boxes {
		if b.Type == "stts" {
			stts = b
		}
	}
	if len(stts.Path) != 5 || stts.Path[4] != "stts" || stts.Path[0] != "moov" {
		t.Fatalf("path = %v", stts.Path)
	}
}

func TestWalkRejectsTruncatedBox(t *testing.T) {
	data := rawBox("ftyp", []byte("isom0000"))
	// Claim a 4 KiB box but only supply its header.
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], 4096)
	copy(hdr[4:], "mdat")
	boxes, err := walkStream(t, append(data, hdr...), Config{})
	if err == nil {
		t.Fatalf("expected truncation error, walked %d boxes", len(boxes))
	}
	assertCorrupt(t, err, "mdat", int64(len(data)))
}

func TestWalkRejectsInvalidLength(t *testing.T) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], 4)
	copy(hdr[4:], "free")
	_, err := walkStream(t, hdr, Config{})
	assertCorrupt(t, err, "free", 0)
	if !errors.Is(err, ErrInvalidLength) {
		t.Fatalf("want ErrInvalidLength, got %v", err)
	}
}

func TestWalkRejectsOverflowing64BitSize(t *testing.T) {
	hdr := make([]byte, 16)
	binary.BigEndian.PutUint32(hdr[:4], 1)
	copy(hdr[4:], "mdat")
	binary.BigEndian.PutUint64(hdr[8:], 1<<63+1)
	_, err := walkStream(t, hdr, Config{})
	assertCorrupt(t, err, "mdat", 0)
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestWalkRejectsChildBeyondContainer(t *testing.T) {
	// moov claims 16 bytes total but holds a 32-byte child.
	child := make([]byte, 32)
	binary.BigEndian.PutUint32(child[:4], 32)
	copy(child[4:], "trak")
	moov := make([]byte, 8)
	binary.BigEndian.PutUint32(moov[:4], 16)
	copy(moov[4:], "moov")
	data := append(moov, child...)
	_, err := walkStream(t, data, Config{})
	assertCorrupt(t, err, "trak", 8)
	if !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("want ErrOutOfBounds, got %v", err)
	}
}

func TestWalkHonoursZeroSizeBox(t *testing.T) {
	// size==0 means "to the end of the input"; it must not exceed it.
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b[:4], 0)
	copy(b[4:], "mdat")
	boxes, err := walkStream(t, b, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 || !boxes[0].ExtendsEOF || boxes[0].Size != 12 {
		t.Fatalf("boxes = %+v", boxes)
	}
}

func TestWalkRejectsDeepNesting(t *testing.T) {
	inner := rawBox("stbl", nil)
	for i := 0; i < 6; i++ {
		inner = rawBox("moov", inner)
	}
	_, err := walkStream(t, inner, Config{MaxDepth: 2})
	if !errors.Is(err, ErrDepth) {
		t.Fatalf("want ErrDepth, got %v", err)
	}
}

func TestWalkRejectsTooManyBoxes(t *testing.T) {
	data := concat(rawBox("free", nil), rawBox("free", nil), rawBox("free", nil))
	_, err := walkStream(t, data, Config{MaxBoxes: 2})
	if !errors.Is(err, ErrTooManyBoxes) {
		t.Fatalf("want ErrTooManyBoxes, got %v", err)
	}
}

func TestWalkCancellation(t *testing.T) {
	s, _ := NewStream(bytes.NewReader(rawBox("ftyp", nil)), 8, "a.mp4", Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Walk(ctx, func(Box) error { return nil })
	var me *model.Error
	if !errors.As(err, &me) || me.Kind != model.KindCancelled {
		t.Fatalf("want cancelled error, got %v", err)
	}
}

func TestPayloadRespectsLimit(t *testing.T) {
	s, _ := NewStream(bytes.NewReader(rawBox("mdat", make([]byte, 64))), 72, "a.mp4", Config{MaxTableBytes: 16})
	boxes, err := walkStream(t, rawBox("mdat", make([]byte, 64)), Config{MaxTableBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	_ = s
	if _, err := s.Payload(boxes[0]); !errors.Is(err, ErrTableTooLarge) {
		t.Fatalf("want ErrTableTooLarge, got %v", err)
	}
}

func TestLargeBoxHeader(t *testing.T) {
	data := largeBox("mdat", make([]byte, 32))
	boxes, err := walkStream(t, data, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if boxes[0].HeaderSize != 16 || boxes[0].Size != 48 {
		t.Fatalf("box = %+v", boxes[0])
	}
}

func assertCorrupt(t *testing.T, err error, boxType string, offset int64) {
	t.Helper()
	var me *model.Error
	if !errors.As(err, &me) {
		t.Fatalf("want structured error, got %v", err)
	}
	if me.Kind != model.KindCorrupt || me.Stage != model.StageMP4Scan {
		t.Fatalf("kind/stage = %s/%s", me.Kind, me.Stage)
	}
	if !me.HasOff || me.Offset != offset {
		t.Fatalf("offset = %d (has=%v), want %d; err=%v", me.Offset, me.HasOff, offset, err)
	}
	if me.Asset != "a.mp4" {
		t.Fatalf("asset = %q", me.Asset)
	}
	_ = boxType
}
