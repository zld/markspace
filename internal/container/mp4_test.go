package container

import (
	"bytes"
	"testing"
)

func TestSegmentContainerRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte{0x0a, 0x00, 0x00, 0x01, 0xff}, 50)
	data, err := BuildSegmentContainer(payload)
	if err != nil {
		t.Fatalf("BuildSegmentContainer: %v", err)
	}
	// A real MP4 box structure starts with the 8-byte ftyp header.
	if len(data) <= len(payload) || string(data[4:8]) != "ftyp" {
		t.Fatalf("container missing ftyp header: %x", data[:min(16, len(data))])
	}
	got, depth, err := ReadSegmentContainer(data)
	if err != nil {
		t.Fatalf("ReadSegmentContainer: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("free-box payload changed after round trip (len %d -> %d)",
			len(payload), len(got))
	}
	if depth != 3 {
		t.Fatalf("nested hierarchy depth = %d, want 3 (mrot/mwrp/mdac)", depth)
	}
}

func TestVerifyRoundTripEmptyAndLarge(t *testing.T) {
	ok, err := VerifyRoundTrip(nil)
	if err != nil || !ok {
		t.Fatalf("empty payload round trip: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyRoundTrip(bytes.Repeat([]byte{1, 2, 3, 4}, 4096))
	if err != nil || !ok {
		t.Fatalf("large payload round trip: ok=%v err=%v", ok, err)
	}
}

func TestCorruptedContainerRejected(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	data, err := BuildSegmentContainer(payload)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Flip the ftyp declared size to an impossible value.
	data[2] = 0xff
	if _, _, err := ReadSegmentContainer(data); err == nil {
		t.Fatal("corrupted box structure must fail parsing")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
