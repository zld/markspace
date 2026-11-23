package mp4

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"

	"mediatool/internal/timeline"
)

func sha256Of(b []byte) [32]byte { return sha256.Sum256(b) }

func box(typ string, payload []byte) []byte {
	var b bytes.Buffer
	size := uint32(8 + len(payload))
	var hdr [4]byte
	hdr[0] = byte(size >> 24)
	hdr[1] = byte(size >> 16)
	hdr[2] = byte(size >> 8)
	hdr[3] = byte(size)
	b.Write(hdr[:])
	b.WriteString(typ)
	b.Write(payload)
	return b.Bytes()
}

func box64(typ string, payload []byte) []byte {
	var b bytes.Buffer
	var hdr [4]byte
	hdr[3] = 1 // size=1 means 64-bit largesize follows
	b.Write(hdr[:])
	b.WriteString(typ)
	var ext [8]byte
	size := uint64(16 + len(payload))
	for i := 0; i < 8; i++ {
		ext[i] = byte(size >> (56 - 8*i))
	}
	b.Write(ext[:])
	b.Write(payload)
	return b.Bytes()
}

func TestScanBoxesBasicOffsets(t *testing.T) {
	data := append(box("moof", []byte{1, 2, 3}), box("mdat", []byte{9, 9})...)
	var got []BoxHeader
	err := ScanBoxes(bytes.NewReader(data), 0, MaxFragmentSize,
		func(b BoxHeader, payload io.Reader) error {
			body, _ := io.ReadAll(payload)
			if b.PayloadOffset != b.Offset+b.HeaderSize {
				t.Fatalf("payload offset mismatch: %+v", b)
			}
			if b.Size != b.HeaderSize+uint64(len(body)) {
				t.Fatalf("size accounting mismatch: %+v", b)
			}
			got = append(got, b)
			return nil
		})
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(got) != 2 || got[0].Type.String() != "moof" || got[1].Type.String() != "mdat" {
		t.Fatalf("unexpected boxes: %+v", got)
	}
	if got[0].Offset != 0 || got[1].Offset != 11 || got[1].PayloadOffset != 19 {
		t.Fatalf("unexpected offsets: %+v", got)
	}
}

func TestScanBoxes64BitLength(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 20)
	data := box64("mdat", payload)
	list, err := CollectBoxes(bytes.NewReader(data), 100, MaxFragmentSize)
	if err != nil {
		t.Fatalf("64-bit box rejected: %v", err)
	}
	if len(list) != 1 || list[0].HeaderSize != 16 || list[0].Size != 36 {
		t.Fatalf("bad 64-bit header: %+v", list)
	}
	if list[0].Offset != 100 || list[0].PayloadOffset != 116 {
		t.Fatalf("bad absolute offsets: %+v", list)
	}
}

func TestScanBoxesTruncatedHeader(t *testing.T) {
	data := []byte{0x00, 0x00, 0x00}
	err := ScanBoxes(bytes.NewReader(data), 0, MaxFragmentSize, func(BoxHeader, io.Reader) error {
		t.Fatal("visitor must not run on truncated header")
		return nil
	})
	me := timeline.AsMediaError(err)
	if me == nil || me.Class != timeline.ClassBoxTruncated || me.Offset != 0 {
		t.Fatalf("want truncated error at offset 0, got %v", err)
	}
}

func TestScanBoxesTruncatedPayload(t *testing.T) {
	// Declares 32 bytes but only contains 8+4.
	data := []byte{0x00, 0x00, 0x00, 0x20, 'm', 'd', 'a', 't', 1, 2, 3, 4}
	err := ScanBoxes(bytes.NewReader(data), 0, MaxFragmentSize, func(BoxHeader, io.Reader) error {
		return nil
	})
	me := timeline.AsMediaError(err)
	if me == nil || me.Class != timeline.ClassBoxTruncated {
		t.Fatalf("want truncated payload error, got %v", err)
	}
}

func TestScanBoxesIllegalLength(t *testing.T) {
	cases := map[string][]byte{
		"size 7": {0x00, 0x00, 0x00, 0x07, 'm', 'd', 'a', 't'},
		"64bit smaller than header": append(
			[]byte{0x00, 0x00, 0x00, 0x01, 'm', 'd', 'a', 't'},
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0f),
	}
	for name, data := range cases {
		err := ScanBoxes(bytes.NewReader(data), 0, MaxFragmentSize, func(BoxHeader, io.Reader) error {
			return nil
		})
		me := timeline.AsMediaError(err)
		if me == nil || me.Class != timeline.ClassBoxLength {
			t.Fatalf("%s: want length error, got %v", name, err)
		}
	}
}

func TestScanBoxesOversizeRejected(t *testing.T) {
	data := box("mdat", make([]byte, 64))
	err := ScanBoxes(bytes.NewReader(data), 0, 60, func(BoxHeader, io.Reader) error {
		t.Fatal("oversize box must not reach visitor")
		return nil
	})
	me := timeline.AsMediaError(err)
	if me == nil || me.Class != timeline.ClassBoxLength {
		t.Fatalf("want length-limit error, got %v", err)
	}
}

func TestScanBoxesExtendToEOF(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	data := append([]byte{0x00, 0x00, 0x00, 0x00, 'm', 'd', 'a', 't'}, payload...)
	var seen BoxHeader
	err := ScanBoxes(bytes.NewReader(data), 0, MaxFragmentSize,
		func(b BoxHeader, r io.Reader) error {
			seen = b
			body, _ := io.ReadAll(r)
			if !bytes.Equal(body, payload) {
				t.Fatalf("payload mismatch: %v", body)
			}
			return nil
		})
	if err != nil || !seen.ExtendToEOF || seen.PayloadSize != 0 {
		t.Fatalf("extend-to-eof handling wrong: %+v err=%v", seen, err)
	}
}

func TestBoxListFirst(t *testing.T) {
	list := BoxList{
		{Type: BoxType{'f', 't', 'y', 'p'}},
		{Type: BoxType{'m', 'o', 'o', 'f'}},
	}
	if b, ok := list.First(BoxType{'m', 'o', 'o', 'f'}); !ok || b.Type.String() != "moof" {
		t.Fatal("first() failed to find moof")
	}
	if _, ok := list.First(BoxType{'t', 'r', 'a', 'f'}); ok {
		t.Fatal("first() must report missing type")
	}
}
