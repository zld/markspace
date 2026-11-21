package slicer

import (
	"bytes"
	"testing"

	"github.com/example/tsaudit/internal/psi"
)

func hdr(pid uint16, pusi bool, cc uint8) [4]byte {
	b1 := byte(pid>>8) & 0x1f
	if pusi {
		b1 |= 0x40
	}
	return [4]byte{0x47, b1, byte(pid), 0x10 | (cc & 0x0f)}
}

func psiPacket(pid uint16, cc uint8, section []byte) [188]byte {
	var p [188]byte
	h := hdr(pid, true, cc)
	copy(p[0:4], h[:])
	p[4] = 0x00
	copy(p[5:], section)
	for i := 5 + len(section); i < 188; i++ {
		p[i] = 0xff
	}
	return p
}

func esPacket(pid uint16, cc uint8) [188]byte {
	var p [188]byte
	h := hdr(pid, false, cc)
	copy(p[0:4], h[:])
	return p
}

func finish(body []byte) []byte {
	s := append([]byte{}, body...)
	s = append(s, 0, 0, 0, 0)
	s[1] = (s[1] & 0xf0) | byte((len(s)-3)>>8)
	s[2] = byte(len(s) - 3)
	crc := psi.CRC32MPEG2(s[:len(s)-4])
	s[len(s)-4] = byte(crc >> 24)
	s[len(s)-3] = byte(crc >> 16)
	s[len(s)-2] = byte(crc >> 8)
	s[len(s)-1] = byte(crc)
	return s
}

func buildStream(t *testing.T) *bytes.Buffer {
	t.Helper()
	pat := finish([]byte{0x00, 0xf0, 0x00, 0x00, 0x01, 0xc1, 0x00, 0x00,
		0x00, 0x01, 0xf0, 0x00})
	pmt0 := finish([]byte{0x02, 0xf0, 0x00, 0x00, 0x01, 0xc1, 0x00, 0x00,
		0xe1, 0x00, 0xf0, 0x00,
		0x1b, 0xe1, 0x00, 0xf0, 0x00, // video 256
		0x0f, 0xe1, 0x01, 0xf0, 0x00}) // audio 257
	pmt1 := finish([]byte{0x02, 0xf0, 0x00, 0x00, 0x01, 0xc3, 0x00, 0x00,
		0xe1, 0x02, 0xf0, 0x00,
		0x1b, 0xe1, 0x02, 0xf0, 0x00, // video 258
		0x0f, 0xe1, 0x01, 0xf0, 0x00})

	var buf bytes.Buffer
	put := func(p [188]byte) { buf.Write(p[:]) }
	put(psiPacket(0, 0, pat))
	put(psiPacket(4096, 0, pmt0))
	for i := 0; i < 5; i++ {
		put(esPacket(256, uint8(i)))
	}
	for i := 0; i < 5; i++ {
		put(esPacket(257, uint8(i)))
	}
	put(psiPacket(4096, 1, pmt1))
	for i := 0; i < 3; i++ {
		put(esPacket(258, uint8(i)))
	}
	// A foreign-PID packet that must never appear in the program 1 slice.
	put(esPacket(999, 0))
	return &buf
}

func TestSliceContainsOnlyProgramPackets(t *testing.T) {
	in := buildStream(t)
	var out bytes.Buffer
	res, err := New(1).Run(in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Output must be a whole number of 188-byte packets.
	if out.Len()%188 != 0 {
		t.Fatalf("slice size %d not a multiple of 188", out.Len())
	}
	n := out.Len() / 188
	// 1 PAT + 2 PMT + 5 + 5 + 3 elementary = 16; foreign packet excluded.
	if n != 16 || res.WrittenPackets != 16 {
		t.Fatalf("written=%d n=%d, want 16", res.WrittenPackets, n)
	}

	// Verify every retained PID belongs to program 1 and ordering is intact.
	allowed := map[uint16]bool{0: true, 4096: true, 256: true, 257: true, 258: true}
	raw := out.Bytes()
	for i := 0; i < n; i++ {
		pkt := raw[i*188 : (i+1)*188]
		pid := uint16(pkt[1]&0x1f)<<8 | uint16(pkt[2])
		if !allowed[pid] {
			t.Fatalf("foreign PID %d retained in slice at packet %d", pid, i)
		}
		if pkt[0] != 0x47 {
			t.Fatalf("packet %d lost sync byte", i)
		}
	}

	// The remap must surface as separate mapping intervals.
	var pids []uint16
	for _, iv := range res.MappingIntervals {
		pids = append(pids, iv.ElementaryPID)
	}
	want := map[uint16]int{256: 1, 257: 2, 258: 1} // 257 spans both versions
	got := map[uint16]int{}
	for _, pid := range pids {
		got[pid]++
	}
	for pid, c := range want {
		if got[pid] != c {
			t.Fatalf("interval count for PID %d = %d, want %d", pid, got[pid], c)
		}
	}
}

func TestSliceUnknownProgramErrors(t *testing.T) {
	in := buildStream(t)
	var out bytes.Buffer
	if _, err := New(7).Run(in, &out); err != ErrProgramNotFound {
		t.Fatalf("err=%v, want ErrProgramNotFound", err)
	}
}
