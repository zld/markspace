package packet

import (
	"bytes"
	"errors"
	"testing"
)

func sample() Frame {
	f := Frame{Version: Version, Seq: 1<<32 + 17, SampleTime: 1_000_000, SendTime: 1234}
	for i := range f.Payload {
		f.Payload[i] = byte(i * 7)
	}
	return f
}

func TestRoundTrip(t *testing.T) {
	in := sample()
	buf := Encode(in)
	if len(buf) != MaxPacketSize {
		t.Fatalf("encoded size = %d, want %d", len(buf), MaxPacketSize)
	}
	out, err := Decode(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch: got %+v want %+v", out, in)
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	good := Encode(sample())

	bad := append([]byte(nil), good...)
	bad[HeaderSize] ^= 0xff
	if _, err := Decode(bad); !errors.Is(err, ErrChecksum) {
		t.Fatalf("payload corruption err = %v, want ErrChecksum", err)
	}

	bad = append([]byte(nil), good...)
	bad[20] ^= 0xff
	if _, err := Decode(bad); !errors.Is(err, ErrChecksum) {
		t.Fatalf("header corruption err = %v, want ErrChecksum", err)
	}

	bad = append([]byte(nil), good...)
	bad[0] = 9
	if _, err := Decode(bad); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("version err = %v, want ErrUnknownVersion", err)
	}

	if _, err := Decode(good[:HeaderSize+2]); !errors.Is(err, ErrTruncated) {
		t.Fatalf("truncation err = %v, want ErrTruncated", err)
	}

	bad = append([]byte(nil), good...)
	bad[16], bad[17], bad[18], bad[19] = 0, 0, 0, 1
	if _, err := Decode(bad); !errors.Is(err, ErrBadLength) {
		t.Fatalf("payload length err = %v, want ErrBadLength", err)
	}
}

func TestSequenceWrap(t *testing.T) {
	f := sample()
	f.Seq = (1 << 40) + 3 // must be masked back to 3 on the wire
	out, err := Decode(Encode(f))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.Seq != 3 {
		t.Fatalf("seq = %d, want 3", out.Seq)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	f := sample()
	if !bytes.Equal(Encode(f), Encode(f)) {
		t.Fatal("encode is not deterministic")
	}
}
