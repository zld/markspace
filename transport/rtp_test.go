package transport

import (
	"testing"

	"github.com/zld/media-check/media"
)

func TestParseRejectsCorruptDatagrams(t *testing.T) {
	cases := [][]byte{
		nil,
		{0x80},
		{0x80, 0x60, 0x00, 0x01}, // header only, no SSRC
	}
	for i, c := range cases {
		if _, err := Parse(c, 0); err == nil {
			t.Errorf("case %d: corrupt packet accepted", i)
		} else if media.ClassOf(err) != media.ClassCorruptPacket {
			t.Errorf("case %d: class = %s, want corrupt_packet", i, media.ClassOf(err))
		}
	}
	// Wrong RTP version in an otherwise complete packet.
	p, err := BuildPacket(1, 1, 0, 96, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	p[0] = 0x40 // version 1
	if _, err := Parse(p, 0); err == nil || media.ClassOf(err) != media.ClassCorruptPacket {
		t.Errorf("version 1 packet: err=%v", err)
	}
}

func TestParseMarshalRoundtrip(t *testing.T) {
	payload := []byte("deterministic payload bytes")
	raw, err := BuildPacket(0x11223344, 65535, 90000, 96, payload)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(raw, 42)
	if err != nil {
		t.Fatal(err)
	}
	if p.Seq != 65535 || p.SSRC != 0x11223344 || p.Timestamp != 90000 || string(p.Payload) != string(payload) {
		t.Fatalf("parsed packet = %+v", p)
	}
	again, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Error("marshal(parse(x)) != x")
	}
}

func TestSeqExtenderWrap(t *testing.T) {
	// The extender anchors the first packet mid-cycle so a preceding-cycle
	// packet can still be placed; extended values are therefore offset from the
	// raw 16-bit counter by a constant.
	const anchor = 0x10000
	se := NewSeqExtender(7)
	seqs := []uint16{65533, 65534, 65535, 0, 1, 2}
	wants := []uint64{anchor + 65533, anchor + 65534, anchor + 65535, anchor + 65536, anchor + 65537, anchor + 65538}
	for i, s := range seqs {
		ext, err := se.Extend(Packet{SSRC: 7, Seq: s})
		if err != nil {
			t.Fatalf("seq %d: %v", s, err)
		}
		if ext != wants[i] {
			t.Errorf("seq %d -> %d, want %d", s, ext, wants[i])
		}
	}
	// Reject a different SSRC.
	if _, err := se.Extend(Packet{SSRC: 8, Seq: 3}); media.ClassOf(err) != media.ClassCorruptPacket {
		t.Errorf("foreign SSRC err = %v", err)
	}
}

func TestSeqExtenderOutOfOrder(t *testing.T) {
	se := NewSeqExtender(1)
	for _, s := range []uint16{100, 101, 103, 102, 104} {
		if _, err := se.Extend(Packet{SSRC: 1, Seq: s}); err != nil {
			t.Fatalf("seq %d: %v", s, err)
		}
	}
	// A late packet from the previous cycle must land before the wrap point.
	// Because the first packet anchors the stream mid-cycle, a genuinely older
	// packet from the preceding cycle maps below the anchor; it must still be
	// accepted (not treated as an underflow) so the reorder stage can declare
	// it late.
	ext, err := se.Extend(Packet{SSRC: 1, Seq: 65534})
	if err != nil {
		t.Fatalf("late previous-cycle packet: %v", err)
	}
	if ext != 65534 {
		t.Errorf("previous-cycle packet ext = %d, want 65534", ext)
	}
	if ext >= 0x10000+100 {
		t.Errorf("previous-cycle packet ext %d did not fall below the anchor", ext)
	}
}

func TestDedupFilterBounded(t *testing.T) {
	d, err := NewDedupFilter(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []uint64{10, 11, 12, 13, 14} {
		if d.Seen(ext) {
			t.Errorf("ext %d falsely reported duplicate", ext)
		}
	}
	for _, ext := range []uint64{10, 11, 14} {
		if !d.Seen(ext) {
			t.Errorf("ext %d not reported duplicate", ext)
		}
	}
	if d.Dropped() != 3 {
		t.Errorf("dropped = %d, want 3", d.Dropped())
	}
	// Entries older than the window are pruned from memory.
	if len(d.seen) > 6 {
		t.Errorf("filter retains %d entries for window 4", len(d.seen))
	}
	if _, err := NewDedupFilter(0); err == nil {
		t.Error("zero window accepted")
	}
}
