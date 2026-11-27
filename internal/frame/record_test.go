package frame

import (
	"bytes"
	"errors"
	"testing"

	"example.com/continuity/internal/fault"
)

func sample() *Record {
	return &Record{
		Version:   Version1,
		Track:     "video/0",
		Seq:       3,
		CaptureNS: 120_000_000,
		Width:     16,
		Height:    8,
		Color:     ColorMeta{2, 1, 1, 1},
		Payload:   bytes.Repeat([]byte{0xAB}, 64),
	}
}

func TestRoundTrip(t *testing.T) {
	raw, err := MarshalRecord(sample())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRecord(raw, 256, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 3 || got.Track != "video/0" || got.CaptureNS != 120_000_000 {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if !bytes.Equal(got.Payload, sample().Payload) || got.Offset != 256 {
		t.Fatal("payload/offset mismatch")
	}
	if got.TotalSize != len(raw) {
		t.Fatalf("size %d != %d", got.TotalSize, len(raw))
	}
}

func TestTruncationReportsOffset(t *testing.T) {
	raw, _ := MarshalRecord(sample())
	for _, cut := range []int{0, 3, 8, len(raw) - 1} {
		_, err := ParseRecord(raw[:cut], 40, 0)
		var fe *fault.Error
		if !errors.As(err, &fe) {
			t.Fatalf("cut=%d: want fault.Error, got %v", cut, err)
		}
		if !errors.Is(err, fault.ErrTruncated) {
			t.Fatalf("cut=%d: %v", cut, err)
		}
		// Offset must be locatable: non-negative and no further than the
		// byte the parser tried to reach inside the full record.
		if fe.Offset < 0 || fe.Offset > 40+int64(len(raw)) {
			t.Fatalf("cut=%d: bad offset %d (record len %d)", cut, fe.Offset, len(raw))
		}
	}
}

func TestChecksumFailure(t *testing.T) {
	raw, _ := MarshalRecord(sample())
	// Flip a payload byte; the digest position must be reported as frame=3.
	raw[len(raw)-ChecksumSize-1] ^= 0x01
	_, err := ParseRecord(raw, 0, 0)
	var fe *fault.Error
	if !errors.As(err, &fe) || !errors.Is(err, fault.ErrChecksum) {
		t.Fatalf("want checksum fault, got %v", err)
	}
	if fe.Frame != 3 || fe.Stage != fault.StageFrame || fe.Track != "video/0" {
		t.Fatalf("fault context wrong: %+v", fe)
	}
	wantOff := int64(len(raw) - ChecksumSize)
	if fe.Offset != wantOff {
		t.Fatalf("offset = %d, want %d", fe.Offset, wantOff)
	}
}

func TestLengthContradiction(t *testing.T) {
	raw, _ := MarshalRecord(sample())
	// Appending trailing bytes makes the consumed length contradict the body.
	_, err := ParseRecord(append(raw, 0xEE), 0, 0)
	if !errors.Is(err, fault.ErrLength) {
		t.Fatalf("want length fault, got %v", err)
	}
}

func TestOversizedPayloadRejected(t *testing.T) {
	rec := sample()
	rec.Payload = make([]byte, 1024)
	raw, _ := MarshalRecord(rec)
	_, err := ParseRecord(raw, 0, 512)
	if !errors.Is(err, fault.ErrLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestBadMagicAndVersion(t *testing.T) {
	raw, _ := MarshalRecord(sample())
	raw[0] = 'X'
	if _, err := ParseRecord(raw, 0, 0); err == nil {
		t.Fatal("bad magic accepted")
	}
	raw2, _ := MarshalRecord(sample())
	raw2[4] = 9
	if _, err := ParseRecord(raw2, 0, 0); err == nil {
		t.Fatal("bad version accepted")
	}
}

func TestZeroDimensions(t *testing.T) {
	rec := sample()
	rec.Width = 0
	raw, _ := MarshalRecord(rec)
	if _, err := ParseRecord(raw, 0, 0); err == nil {
		t.Fatal("zero width accepted")
	}
}
