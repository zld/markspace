package ingest

import (
	"audionorm/internal/media"
	"bytes"
	"testing"

	"github.com/pion/rtp"
)

// buildRTPPacket assembles a minimal RTP packet carrying an Opus TOC
// for a 20 ms single frame (config 12 -> 20 ms FB SILK, code 0).
func buildRTPPacket(t *testing.T, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, PayloadType: 111, SSRC: 1},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func opus20ms() []byte {
	// TOC config=13 (20 ms FB), stereo bit set, code 0 (one frame); 1 dummy byte.
	return []byte{0x68 | 0x04, 0xff}
}

func TestRTPMissingAndDuplicate(t *testing.T) {
	in := NewRTPIngester(CodecOpus, 48000, 2)
	// seq 0..39 good, seq 40 dropped, seq 41.. good, seq 42 sent twice.
	const step = 960 // 20 ms at 48 kHz RTP clock
	for seq := uint16(0); seq < 40; seq++ {
		if err := in.Push(buildRTPPacket(t, seq, uint32(seq)*step, opus20ms()), int64(seq)*100); err != nil {
			t.Fatal(err)
		}
	}
	// seq 40 missing; send 41..
	for seq := uint16(41); seq < 44; seq++ {
		if err := in.Push(buildRTPPacket(t, seq, uint32(seq)*step, opus20ms()), int64(seq)*100); err != nil {
			t.Fatal(err)
		}
	}
	// duplicate of 42
	if err := in.Push(buildRTPPacket(t, 42, 42*step, opus20ms()), 9999); err != nil {
		t.Fatal(err)
	}
	res, err := in.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", res.Duplicates)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps = %d, want 1", len(res.Gaps))
	}
	g := res.Gaps[0]
	if g.When.Start != 40*step || g.When.End != 41*step {
		t.Fatalf("gap interval = [%d,%d), want [%d,%d)", g.When.Start, g.When.End, 40*step, 41*step)
	}
	// 43 accepted packets (0..39, 41..43) each 960 samples.
	if len(res.Packets) != 43 {
		t.Fatalf("packets = %d, want 43", len(res.Packets))
	}
	if last := res.Packets[len(res.Packets)-1]; last.When.End != 44*step {
		t.Fatalf("last packet end = %d, want %d", last.When.End, 44*step)
	}
}

func TestRTPSequenceWraparound(t *testing.T) {
	in := NewRTPIngester(CodecOpus, 48000, 1)
	seqs := []uint16{0xfffe, 0xffff, 0x0000, 0x0001}
	for i, seq := range seqs {
		ts := uint32(i * 960)
		if err := in.Push(buildRTPPacket(t, seq, ts, opus20ms()), 0); err != nil {
			t.Fatal(err)
		}
	}
	res, _ := in.Close()
	if len(res.Packets) != 4 || len(res.Gaps) != 0 {
		t.Fatalf("wrap handling broken: packets=%d gaps=%d", len(res.Packets), len(res.Gaps))
	}
	// First and last packet must sit on contiguous intervals.
	if res.Packets[0].When.Start != 0 || res.Packets[3].When.End != 4*960 {
		t.Fatal("wrap timeline not contiguous")
	}
}

func TestRTPCorruptPacketHolds(t *testing.T) {
	in := NewRTPIngester(CodecOpus, 48000, 2)
	if err := in.Push(buildRTPPacket(t, 0, 0, opus20ms()), 0); err != nil {
		t.Fatal(err)
	}
	// Empty payload -> corrupt, unlocatable.
	if err := in.Push(buildRTPPacket(t, 1, 960, []byte{}), 100); err != nil {
		t.Fatal(err)
	}
	if err := in.Push(buildRTPPacket(t, 2, 1920, opus20ms()), 200); err != nil {
		t.Fatal(err)
	}
	res, _ := in.Close()
	if !res.Errors.Has(media.ErrCorruptPacket) {
		t.Fatal("corrupt packet not recorded")
	}
	rec := res.Errors.All()[0]
	if rec.Locatable || rec.ByteOffset != 100 {
		t.Fatalf("corrupt record should be pending with offset 100, got %+v", rec)
	}
}

func TestRTPRejectsBadLayoutAndOversize(t *testing.T) {
	in := NewRTPIngester(CodecOpus, 48000, 6)
	if err := in.Push(buildRTPPacket(t, 0, 0, opus20ms()), 0); err != nil {
		t.Fatal(err)
	}
	res, _ := in.Close()
	if !res.Errors.Has(media.ErrUnsupportedLayout) {
		t.Fatal("6-channel layout must be rejected")
	}
	big := bytes.Repeat([]byte{0xff}, media.MaxPacketBytes+1)
	in2 := NewRTPIngester(CodecOpus, 48000, 2)
	if err := in2.Push(big, 7); err != nil {
		t.Fatal(err)
	}
	if !in2.res.Errors.Has(media.ErrPacketTooLarge) {
		t.Fatal("oversize packet must be rejected")
	}
}

func TestOpusDurationTOC(t *testing.T) {
	cases := []struct {
		toc  byte
		want float64
	}{
		{0x00, 0.01},        // config 0, 10 ms, one frame
		{0x68, 0.02},        // config 13, 20 ms, one frame
		{0x69, 0.04},        // two equal 20 ms frames
		{0x03, 0},           // arbitrary needs count byte -> error below
	}
	d, err := OpusDuration([]byte{cases[0].toc, 0xaa})
	if err != nil || d != cases[0].want {
		t.Fatalf("case0 d=%v err=%v", d, err)
	}
	d, _ = OpusDuration([]byte{cases[1].toc, 0xaa})
	if d != cases[1].want {
		t.Fatalf("case1 d=%v", d)
	}
	d, _ = OpusDuration([]byte{cases[2].toc, 0xaa})
	if d != cases[2].want {
		t.Fatalf("case2 d=%v", d)
	}
	// arbitrary with M byte declaring 3 frames of 20 ms
	d, err = OpusDuration([]byte{0x6b, 0x03, 0xaa, 0xbb})
	if err != nil || d != 0.06 {
		t.Fatalf("arbitrary d=%v err=%v", d, err)
	}
	if _, err := OpusDuration(nil); err == nil {
		t.Fatal("empty payload must error")
	}
	_ = cases[3]
}

func TestAACRTPValidation(t *testing.T) {
	// One AU of 4 bytes: AU-headers-length=16, header=(index0,size4)=0x0004.
	good := []byte{0x00, 0x10, 0x00, 0x04, 0xde, 0xad, 0xbe, 0xef}
	if err := validateAACRTP(good); err != nil {
		t.Fatalf("valid AU rejected: %v", err)
	}
	if err := validateAACRTP([]byte{0x00}); err == nil {
		t.Fatal("truncated payload accepted")
	}
	if err := validateAACRTP([]byte{0x00, 0x10, 0x00, 0x00}); err == nil {
		t.Fatal("zero-size AU accepted")
	}
	if err := validateAACRTP([]byte{0x00, 0x10, 0x00, 0x09, 0x01}); err == nil {
		t.Fatal("declared AU data missing accepted")
	}
}
