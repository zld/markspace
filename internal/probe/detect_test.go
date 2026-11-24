package probe

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp/v2"
)

func TestDetectMP4AndMOV(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name  string
		brand []byte
		ext   string
		want  string
	}{
		{"a.mp4", []byte("isom"), ".mp4", FormatMP4},
		{"a.mov", []byte("qt  "), ".mov", FormatMOV},
	} {
		path := filepath.Join(dir, tc.name)
		box := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p'}
		box = append(box, tc.brand...)
		box = append(box, 0, 0, 0, 0)
		if err := os.WriteFile(path, box, 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := Detect(path)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if info.Format != tc.want {
			t.Fatalf("%s format=%s want %s", tc.name, info.Format, tc.want)
		}
	}
}

func TestDetectLengthPrefixedRTP(t *testing.T) {
	pkt := &rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 111, SSRC: 0x12345678,
		SequenceNumber: 7, Timestamp: 99,
	}, Payload: []byte("compressed-audio")}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)
	path := filepath.Join(t.TempDir(), "a.rtp")
	if err := os.WriteFile(path, frame, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != FormatRTP || info.RTPPacketCount != 1 {
		t.Fatalf("info=%+v", info)
	}
}

func TestDetectUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bin")
	if err := os.WriteFile(path, []byte("not media at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(path); err == nil {
		t.Fatal("expected probe error")
	}
}
