package ts

import "testing"

// encodeTimestamp produces the 5-byte marker-padded PTS/DTS representation.
func encodeTimestamp(ts uint64) []byte {
	return []byte{
		0x21 | byte((ts>>30)&0x07)<<1,
		byte(ts >> 22),
		byte((ts>>14)&0xfe) | 0x01,
		byte(ts >> 7),
		byte((ts&0x7f)<<1) | 0x01,
	}
}

// mkPES builds a standards-conformant PTS-only PES packet. The optional
// PES header uses flags 0x80 (PTS only, markers set) with a 5-byte length.
func mkPES(streamID byte, pts uint64, payload []byte) []byte {
	opt := encodeTimestamp(pts)
	hdr := []byte{
		0x00, 0x00, 0x01, streamID,
		0, 0, // PES_packet_length, filled below
		0x80,    // flags1: PTS_DTS_flags = 10b
		0x00,    // flags2
		byte(5), // PES_header_data_length = 5 for one timestamp
	}
	hdr = append(hdr, opt...)
	body := append(hdr, payload...)
	// PES_packet_length counts everything after its own two bytes.
	n := len(body) - 6
	body[4] = byte(n >> 8)
	body[5] = byte(n)
	return body
}

func TestParsePESPTS(t *testing.T) {
	const pts = uint64(270_000) // 3 seconds at 90 kHz
	pes := mkPES(0xe0, pts, []byte{0xaa, 0xbb, 0xcc})
	h, err := ParsePES(pes)
	if err != nil {
		t.Fatal(err)
	}
	if !h.HasPTS || h.HasDTS {
		t.Fatalf("flags: pts=%v dts=%v", h.HasPTS, h.HasDTS)
	}
	if h.PTS != pts {
		t.Fatalf("pts: got %d, want %d", h.PTS, pts)
	}
	if len(h.Data) != 3 || h.Data[0] != 0xaa {
		t.Fatalf("payload: %x", h.Data)
	}
}

func TestParsePESRoundTripTimestamps(t *testing.T) {
	for _, pts := range []uint64{0, 3000, 1<<33 - 1, 123_456_789} {
		got := decodeTimestamp(encodeTimestamp(pts))
		if got != pts {
			t.Fatalf("timestamp round trip %d -> %d", pts, got)
		}
	}
}

func TestParsePESStartCodeAndTruncation(t *testing.T) {
	bad := []byte{1, 2, 3, 4}
	if _, err := ParsePES(bad); err == nil {
		t.Fatal("missing start code must error")
	}
	short := mkPES(0xe0, 0, nil)[:5]
	h, err := ParsePES(short)
	if err == nil || !h.Truncated {
		t.Fatalf("short PES must be flagged truncated: %v %v", h, err)
	}
}

func TestH264KeyframeDetection(t *testing.T) {
	idr := append([]byte{0x00, 0x00, 0x00, 0x01, H264NALIDR, 0x88}, make([]byte, 10)...)
	if !IsH264Keyframe(idr) {
		t.Fatal("IDR NAL must be detected as keyframe")
	}
	sps := append([]byte{0x00, 0x00, 0x01, H264NALSPS, 0x42}, make([]byte, 8)...)
	if !IsH264Keyframe(sps) {
		t.Fatal("SPS NAL must be detected as keyframe")
	}
	nonKey := append([]byte{0x00, 0x00, 0x01, 0x01, 0x9a}, make([]byte, 8)...)
	if IsH264Keyframe(nonKey) {
		t.Fatal("non-IDR NAL must not be a keyframe")
	}
	// SPS followed by IDR in the same PES.
	combined := append(sps, idr...)
	if !IsH264Keyframe(combined) {
		t.Fatal("combined NAL units must detect the IDR")
	}
}

// fragmentPES wraps a PES unit in TS packets on pid, each carrying at most
// 184 payload bytes; only the first packet has PUSI set. A short final
// fragment is padded with an adaptation field, as a real muxer would do.
func fragmentPES(t *testing.T, pid uint16, pes []byte) []Packet {
	t.Helper()
	const cap = PacketSize - 4
	var pkts []Packet
	cc := uint8(0)
	for off := 0; off < len(pes); off += cap {
		n := len(pes) - off
		if n > cap {
			n = cap
		}
		last := off+n >= len(pes)
		var pb []byte
		if last && n < cap {
			// Adaptation field fills the unused payload space.
			pad := cap - n
			pb = make([]byte, PacketSize)
			pb[0] = SyncByte
			if off == 0 {
				pb[1] = 0x40
			}
			pb[1] |= byte(pid >> 8)
			pb[2] = byte(pid)
			pb[3] = 0x30 | (cc & 0x0f) // adaptation + payload
			pb[4] = byte(pad - 1)      // adaptation_field_length
			copy(pb[4+pad:], pes[off:off+n])
		} else {
			pb = mkPacket(pid, off == 0, cc&0x0f, 0)
			copy(pb[4:4+n], pes[off:off+n])
		}
		cc++
		p, err := ParsePacket(pb)
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, p)
	}
	return pkts
}

func TestPESReassembler(t *testing.T) {
	r := NewPESReassembler(0)
	pes := mkPES(0xe0, 9000, make([]byte, 300))
	for i, p := range fragmentPES(t, 0x100, pes) {
		completed, start := r.Push(p)
		if i == 0 && !start {
			t.Fatal("first fragment must mark start")
		}
		if completed != nil {
			t.Fatal("unit completed before PUSI boundary")
		}
	}
	out := r.Drain()
	if len(out) != len(pes) {
		t.Fatalf("reassembled length: got %d, want %d", len(out), len(pes))
	}
	h, err := ParsePES(out)
	if err != nil {
		t.Fatal(err)
	}
	if h.PTS != 9000 {
		t.Fatalf("pts: %d", h.PTS)
	}
}

func TestPESReassemblerBoundaryFlush(t *testing.T) {
	r := NewPESReassembler(0)
	first := mkPES(0xe0, 3000, []byte{1, 2})
	second := mkPES(0xe0, 6000, []byte{3, 4})
	for _, p := range fragmentPES(t, 0x100, first) {
		if got, _ := r.Push(p); got != nil {
			t.Fatal("nothing should be flushed on the first PUSI")
		}
	}
	pkts := fragmentPES(t, 0x100, second)
	got, start := r.Push(pkts[0])
	if !start {
		t.Fatal("second unit must mark a new start")
	}
	if len(got) != len(first) {
		t.Fatalf("boundary flush length: got %d, want %d", len(got), len(first))
	}
}

func TestPESReassemblerCap(t *testing.T) {
	// A unit larger than the cap must be truncated, not grown unbounded.
	r := NewPESReassembler(256)
	big := mkPES(0xe0, 0, make([]byte, 1000))
	for _, p := range fragmentPES(t, 0x100, big) {
		r.Push(p)
	}
	if out := r.Drain(); len(out) != 256 {
		t.Fatalf("cap: got %d bytes", len(out))
	}
}
