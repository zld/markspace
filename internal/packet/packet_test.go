package packet

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	pcm := []int16{0, 100, -100, 32767, -32768}
	buf, err := Encode(42, 480*7, EncodePCM(pcm))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	p, err := Decode(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.Seq != 42 || p.Timestamp != 480*7 {
		t.Fatalf("header mismatch: seq=%d ts=%d", p.Seq, p.Timestamp)
	}
	got, err := DecodePCM(p.Payload)
	if err != nil {
		t.Fatalf("DecodePCM: %v", err)
	}
	if len(got) != len(pcm) {
		t.Fatalf("sample count %d != %d", len(got), len(pcm))
	}
	for i := range pcm {
		if got[i] != pcm[i] {
			t.Fatalf("sample %d: %d != %d", i, got[i], pcm[i])
		}
	}
}

func TestDecodeRejectsTruncated(t *testing.T) {
	buf, _ := Encode(1, 0, []byte{1, 2, 3, 4})
	for _, n := range []int{0, 5, HeaderSize - 1, HeaderSize + 1} {
		if _, err := Decode(buf[:n]); err == nil {
			t.Fatalf("len %d: expected error", n)
		}
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	buf, _ := Encode(1, 0, []byte{1, 2, 3, 4})
	bad := bytes.Clone(buf)
	bad[3] ^= 0xFF // corrupt seq -> CRC mismatch
	if _, err := Decode(bad); err != ErrBadCRC {
		t.Fatalf("want ErrBadCRC, got %v", err)
	}
	bad = bytes.Clone(buf)
	bad[0] = 0x00 // bad magic
	if _, err := Decode(bad); err != ErrBadMagic {
		t.Fatalf("want ErrBadMagic, got %v", err)
	}
}

func TestDecodeRejectsOversize(t *testing.T) {
	if _, err := Decode(make([]byte, MaxPacket+1)); err != ErrTooLong {
		t.Fatalf("want ErrTooLong, got %v", err)
	}
	if _, err := Encode(0, 0, make([]byte, MaxPayload+1)); err != ErrTooLong {
		t.Fatalf("encode: want ErrTooLong, got %v", err)
	}
}

func TestDecodePCMOddLength(t *testing.T) {
	if _, err := DecodePCM([]byte{1}); err == nil {
		t.Fatal("expected error for odd payload")
	}
}
