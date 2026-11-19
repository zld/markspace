package ts

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReaderOffsetAndTail(t *testing.T) {
	pkt := make([]byte, PacketSize*2)
	for i := range pkt {
		pkt[i] = 0xff
	}
	for n := 0; n < 2; n++ {
		base := n * PacketSize
		pkt[base] = 0x47
		pkt[base+1] = 0x00
		pkt[base+2] = 0x00
		pkt[base+3] = 0x10 | byte(n&0xf)
		pkt[base+4] = 0x22
	}
	bs := append([]byte{0xde, 0xad, 0xbe, 0xef}, pkt...)
	bs = append(bs, 0x47, 0x00)
	path := filepath.Join(t.TempDir(), "a.ts")
	if err := os.WriteFile(path, bs, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var ps []*Packet
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ps = append(ps, p)
	}
	if len(ps) != 2 {
		t.Fatalf("got %d packets, want 2", len(ps))
	}
	if ps[0].Offset != 4 || ps[1].Offset != 4+PacketSize {
		t.Fatalf("unexpected offsets: %d, %d", ps[0].Offset, ps[1].Offset)
	}
	if r.TrailingPackets() != 1 || r.TrailingBytes() != 2 {
		t.Fatalf("trailing = %d/%d, want 1/2", r.TrailingPackets(), r.TrailingBytes())
	}
}

func TestReaderRejectsBadSync(t *testing.T) {
	bad := make([]byte, PacketSize*2)
	for i := range bad {
		bad[i] = 0x00
	}
	path := filepath.Join(t.TempDir(), "bad.ts")
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.Next()
	var se *SyncError
	if !errors.As(err, &se) {
		t.Fatalf("got %v, want *SyncError", err)
	}
}

func TestReaderRawPackets(t *testing.T) {
	pkt := make([]byte, PacketSize*2)
	for i := range pkt {
		pkt[i] = 0xab
	}
	for n := 0; n < 2; n++ {
		base := n * PacketSize
		pkt[base] = 0x47
		pkt[base+1] = 0x01
		pkt[base+2] = 0x00
		pkt[base+3] = 0x10 | byte(n)
	}
	path := filepath.Join(t.TempDir(), "raw.ts")
	if err := os.WriteFile(path, pkt, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for n := 0; n < 2; n++ {
		p, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Raw) != PacketSize {
			t.Fatalf("packet %d raw length %d", n, len(p.Raw))
		}
		if p.Raw[1] != 0x01 {
			t.Fatalf("packet %d raw bytes do not match input: %x", n, p.Raw[:4])
		}
	}
}
