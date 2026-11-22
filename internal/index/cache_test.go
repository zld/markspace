package index

import (
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/clock"
)

func sampleDigest(t *testing.T) string {
	t.Helper()
	h := sha256Sum("fixture-bytes")
	return h
}

func TestRecordEncodeDecodeRoundTrip(t *testing.T) {
	r := Record{
		Offset: 0x1122334455667788, Size: 1234, WireSize: 5678,
		DTS: -999, DTSNanos: -123456789, CTSNanos: 987654321, Flags: flagSync,
	}
	var buf [recordSize]byte
	r.Encode(buf[:])
	back := Decode(buf[:])
	if back != r {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", r, back)
	}
	if !back.IsSync() {
		t.Fatal("sync flag lost")
	}
}

func TestCacheWriteVerifyAndTamper(t *testing.T) {
	dir := t.TempDir()
	digest := sampleDigest(t)
	path := filepath.Join(dir, "idx.bin")
	hdr := Header{
		SchemaVersion: SchemaVersion, TrackID: 7, Timescale: 90000, SampleCount: 3,
	}
	copy(hdr.Magic[:], magic)
	decoded, err := hexDecode32(digest)
	if err != nil {
		t.Fatal(err)
	}
	copy(hdr.InputDigest[:], decoded)

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := newStreamWriter(f, hdr)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		{Offset: 100, Size: 8, WireSize: 1500, DTS: 0, DTSNanos: 1, CTSNanos: 1},
		{Offset: 108, Size: 8, WireSize: 1600, DTS: 3000, DTSNanos: 2, CTSNanos: 2, Flags: flagSync},
		{Offset: 116, Size: 8, WireSize: 1400, DTS: 6000, DTSNanos: 3, CTSNanos: 3},
	}
	for _, r := range records {
		if err := w.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	wantDigest, err := w.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	info, _ := os.Stat(path)
	if info.Size() != ExpectedSize(3) {
		t.Fatalf("size=%d want %d", info.Size(), ExpectedSize(3))
	}
	got, err := LoadOrVerify(path, digest, 7, 3)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TrackID != 7 {
		t.Fatal("header track mismatch")
	}
	rf, _ := os.Open(path)
	defer rf.Close()
	r1, err := ReadRecord(rf, got, 1)
	if err != nil || r1.Offset != 108 || !r1.IsSync() {
		t.Fatalf("record read wrong: %+v err=%v", r1, err)
	}
	if _, err := ReadRecord(rf, got, 3); clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("out-of-range read not rejected: %v", err)
	}
	if Digest(got, records) != wantDigest {
		t.Fatal("canonical digest mismatch")
	}

	// Tamper a record byte: trailer verification must fail.
	data, _ := os.ReadFile(path)
	data[headerSize+5] ^= 0xff
	os.WriteFile(path, data, 0o644)
	if _, err := LoadOrVerify(path, digest, 7, 3); clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("tamper not detected: %v", err)
	}
}

func TestTruncatedCacheRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	os.WriteFile(path, make([]byte, headerSize+recordSize), 0o644) // missing trailer
	_, err := LoadOrVerify(path, sampleDigest(t), 1, 1)
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("want recoverable, got %v", err)
	}
}
