package rtplog

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"rtpreplay/media"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHeader(&buf); err != nil {
		t.Fatal(err)
	}
	arrival := time.UnixMicro(1_700_000_000_000_000)
	packets := [][]byte{{1, 2, 3}, {4, 5}, bytes.Repeat([]byte{9}, 100)}
	for _, p := range packets {
		if err := WriteRecord(&buf, arrival, p); err != nil {
			t.Fatal(err)
		}
	}
	rd, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range packets {
		rec, err := rd.Next()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if rec.Index != i || !bytes.Equal(rec.Packet, want) || !rec.Arrival.Equal(arrival) {
			t.Fatalf("record %d mismatch: %+v", i, rec)
		}
	}
	if _, err := rd.Next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestBadMagic(t *testing.T) {
	_, err := NewReader(strings.NewReader("XXXXXXXX"), 8)
	if le, ok := media.AsLocatable(err); !ok || le.Code != "BAD_MAGIC" {
		t.Fatalf("want BAD_MAGIC, got %v", err)
	}
}

func TestTruncatedHeaderAndPayload(t *testing.T) {
	// Header present but record header cut short.
	var buf bytes.Buffer
	_ = WriteHeader(&buf)
	buf.Write([]byte{0x05, 0x00}) // declares payload 5 but only 2 header bytes
	rd, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rd.Next(); media.MustLocatable(t, err).Code != "TRUNCATED_RECORD_HEADER" {
		t.Fatalf("got %v", err)
	}

	// Full record header but payload truncated.
	var buf2 bytes.Buffer
	_ = WriteHeader(&buf2)
	var hdr [12]byte
	le.PutUint32(hdr[:4], 50)
	le.PutUint64(hdr[4:], 0)
	buf2.Write(hdr[:])
	buf2.Write([]byte{1, 2, 3})
	rd2, _ := NewReader(bytes.NewReader(buf2.Bytes()), int64(buf2.Len()))
	if _, err = rd2.Next(); media.MustLocatable(t, err).Code != "TRUNCATED_RECORD_PAYLOAD" {
		t.Fatalf("got %v", err)
	}
}

func TestLengthOutOfBounds(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteHeader(&buf)
	var hdr [12]byte
	le.PutUint32(hdr[:4], uint32(media.MaxPacketBytes+1))
	buf.Write(hdr[:])
	rd, _ := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	_, err := rd.Next()
	if media.MustLocatable(t, err).Code != "LENGTH_OUT_OF_BOUNDS" {
		t.Fatalf("got %v", err)
	}
}

func TestInputTooLarge(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteHeader(&buf)
	_, err := NewReader(bytes.NewReader(buf.Bytes()), media.MaxInputBytes+1)
	if media.MustLocatable(t, err).Code != "INPUT_TOO_LARGE" {
		t.Fatalf("got %v", err)
	}
}
