package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"replayctl/internal/fault"
)

func TestScanTruncatedBoxReportsFirstUnreadableOffset(t *testing.T) {
	var data [16]byte
	binary.BigEndian.PutUint32(data[0:4], 64)
	copy(data[4:8], "ftyp")
	_, err := Scan(bytes.NewReader(data[:]), int64(len(data)), "truncated.mp4")
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("error %T is not fault.Error", err)
	}
	if fe.Code != fault.TruncatedBox || fe.Offset != 16 {
		t.Fatalf("fault=%+v, want TRUNCATED_BOX offset=16", fe)
	}
}

func TestScanRejectsSampleOffsetBeyondFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample-offset.mp4")
	track := TrackIn{
		Kind: "audio", Codec: "sowt", Timescale: 48000, Channels: 1, Bits: 16, SampleRate: 48000,
		Data: []byte{1, 0, 2, 0}, DefaultSampleSize: 2, SampleCount: 2, Delta: 1,
	}
	if err := MuxFile(path, []TrackIn{track}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pos := bytes.Index(data, []byte("stco"))
	if pos < 0 {
		t.Fatal("stco box not found")
	}
	offsetPos := pos + 12 // type(4) + version/flags(4) + entry_count(4)
	binary.BigEndian.PutUint32(data[offsetPos:offsetPos+4], uint32(len(data)+4096))
	corrupt := filepath.Join(dir, "corrupt.mp4")
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = ScanFile(corrupt)
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.SampleOffset {
		t.Fatalf("err=%v, want SAMPLE_OFFSET", err)
	}
	if fe.Offset != int64(len(data)+4096) {
		t.Fatalf("offset=%d want invalid sample offset", fe.Offset)
	}
}
