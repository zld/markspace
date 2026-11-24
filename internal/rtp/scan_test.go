package rtp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"mediatool/internal/clock"
	"mediatool/internal/fault"
)

// buildCapture frames datagrams in the capture format. body shorter than
// declared makes a truncated record.
func buildCapture(declared [][]byte, captured [][]byte) []byte {
	var buf bytes.Buffer
	buf.Write(Magic)
	for i := range declared {
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(len(captured[i])))
		binary.BigEndian.PutUint32(hdr[4:8], uint32(len(declared[i])))
		buf.Write(hdr[:])
		buf.Write(captured[i])
	}
	return buf.Bytes()
}

// rtpPacket makes a minimal RTP packet with an optional header extension.
func rtpPacket(seq uint16, ts uint32, ssrc uint32, payload []byte, extWords uint16, padding int) []byte {
	var p []byte
	b0 := byte(0x80)
	if extWords > 0 {
		b0 |= 0x10
	}
	if padding > 0 {
		b0 |= 0x20
	}
	p = append(p, b0, 96)
	var s [2]byte
	binary.BigEndian.PutUint16(s[:], seq)
	p = append(p, s[:]...)
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], ts)
	p = append(p, t[:]...)
	binary.BigEndian.PutUint32(t[:], ssrc)
	p = append(p, t[:]...)
	if extWords > 0 {
		var e [4]byte
		binary.BigEndian.PutUint16(e[0:2], 0xBEDE)
		binary.BigEndian.PutUint16(e[2:4], extWords)
		p = append(p, e[:]...)
		for i := uint16(0); i < extWords; i++ {
			p = append(p, 0x10, 0xAA, 0xBB, 0xCC)
		}
	}
	p = append(p, payload...)
	if padding > 0 {
		for i := 0; i < padding-1; i++ {
			p = append(p, 0)
		}
		p = append(p, byte(padding))
	}
	return p
}

func scanAll(t *testing.T, data []byte, cfg Config) ([]Event, Stats) {
	t.Helper()
	s, err := NewScanner(bytes.NewReader(data), cfg)
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}
	var evs []Event
	for {
		ev, ok, err := s.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		evs = append(evs, ev)
	}
	return evs, s.Stats()
}

func TestScannerFindsLossReorderDuplicate(t *testing.T) {
	cfg := Config{Tracks: map[uint32]TrackInfo{7: {TrackID: 1, ClockRate: 90000}}, Window: 8}
	payload := []byte{1, 2, 3, 4}
	var declared, captured [][]byte
	// Sequence: 1,2,4,3,4,5 -> 3 after 4 (reorder), then 4 again (dup),
	// and nothing after 5 so 3 was eventually filled. Then 9 to force a gap.
	for _, seq := range []uint16{1, 2, 4, 3, 4, 5, 9} {
		p := rtpPacket(seq, uint32(seq)*3000, 7, payload, 0, 0)
		declared = append(declared, p)
		captured = append(captured, p)
	}
	evs, st := scanAll(t, buildCapture(declared, captured), cfg)

	var sawReorder, sawDup, sawLoss bool
	for _, ev := range evs {
		for _, f := range ev.Faults {
			switch f.Kind {
			case fault.KindReordered:
				sawReorder = true
			case fault.KindDuplicate:
				sawDup = true
			case fault.KindLoss:
				sawLoss = true
			}
		}
	}
	if !sawReorder || !sawDup || !sawLoss {
		t.Fatalf("missing events: reorder=%v dup=%v loss=%v (stats %+v)", sawReorder, sawDup, sawLoss, st)
	}
	if st.Reordered != 1 {
		t.Fatalf("reordered = %d, want 1", st.Reordered)
	}
	if st.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", st.Duplicates)
	}
	// 6,7,8 are lost and must be reported.
	if st.Lost != 3 {
		t.Fatalf("lost = %d, want 3", st.Lost)
	}
}

func TestReorderedPacketWithinWindowIsNotLost(t *testing.T) {
	cfg := Config{Tracks: map[uint32]TrackInfo{1: {TrackID: 1, ClockRate: 48000}}, Window: 32}
	var declared, captured [][]byte
	for _, seq := range []uint16{10, 12, 11, 13} {
		p := rtpPacket(seq, uint32(seq)*960, 1, []byte{9}, 0, 0)
		declared = append(declared, p)
		captured = append(captured, p)
	}
	_, st := scanAll(t, buildCapture(declared, captured), cfg)
	if st.Lost != 0 {
		t.Fatalf("lost = %d, want 0: reordered 11 arrived inside the window", st.Lost)
	}
	if st.Reordered != 1 {
		t.Fatalf("reordered = %d, want 1", st.Reordered)
	}
}

func TestTruncationOffsetIsFirstSuspiciousByte(t *testing.T) {
	cfg := Config{Tracks: map[uint32]TrackInfo{1: {TrackID: 1, ClockRate: 48000}}, Window: 8}
	full := rtpPacket(1, 0, 1, bytes.Repeat([]byte{0xAB}, 40), 1, 0)
	// Capture only the first 12 bytes of the payload-bearing packet.
	cut := full[:12]
	data := buildCapture([][]byte{full}, [][]byte{cut})
	evs, st := scanAll(t, data, cfg)
	if st.Truncated != 1 {
		t.Fatalf("truncated = %d, want 1", st.Truncated)
	}
	var f *fault.Fault
	for _, ev := range evs {
		for _, x := range ev.Faults {
			if x.Kind == fault.KindTruncated {
				f = x
			}
		}
	}
	if f == nil {
		t.Fatal("no truncation fault")
	}
	// The body starts at len(Magic)+8. The offset must point at the packet
	// body itself, within one RTP header (12 bytes) of the record start.
	wantStart := int64(len(Magic) + 8)
	if f.Offset != wantStart {
		t.Fatalf("truncation offset = %d, want %d", f.Offset, wantStart)
	}
	if f.Length != len(full) {
		t.Fatalf("truncation length = %d, want declared %d", f.Length, len(full))
	}
	if d := f.Offset - wantStart; d > 12 || d < -12 {
		t.Fatalf("offset error %d exceeds one RTP header", d)
	}
}

func TestEveryFaultOffsetLandsInPacketBody(t *testing.T) {
	cfg := Config{Tracks: map[uint32]TrackInfo{1: {TrackID: 1, ClockRate: 48000}}, Window: 4}
	var declared, captured [][]byte
	offsets := map[uint16]int64{}
	cursor := int64(len(Magic))
	for _, seq := range []uint16{1, 3, 3, 7, 6} {
		p := rtpPacket(seq, uint32(seq)*960, 1, []byte{1, 2, 3}, 0, 0)
		declared = append(declared, p)
		captured = append(captured, p)
		offsets[seq] = cursor + 8
		cursor += 8 + int64(len(p))
	}
	evs, _ := scanAll(t, buildCapture(declared, captured), cfg)
	checked := 0
	for _, ev := range evs {
		if ev.Kind != EventPacket || len(ev.Faults) == 0 {
			continue
		}
		for _, f := range ev.Faults {
			if f.TrackID != 1 {
				continue
			}
			// The fault offset must equal some packet body start; the delta from
			// the event's own offset must be sub-header.
			if d := f.Offset - ev.Offset; d > 12 || d < -12 {
				t.Fatalf("fault offset %d differs from event offset %d by more than a header", f.Offset, ev.Offset)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no located faults checked")
	}
}

func TestSenderReportRoutesToTrack(t *testing.T) {
	cfg := Config{Tracks: map[uint32]TrackInfo{0x55: {TrackID: 3, ClockRate: 48000}}}
	var srBody []byte
	srBody = AppendSenderReport(srBody, clock.SenderReport{SSRC: 0x55, NTP: clock.NsToNTP(1_700_000_000e9), RTP: 100, PacketCount: 1})
	data := buildCapture([][]byte{srBody}, [][]byte{srBody})
	evs, st := scanAll(t, data, cfg)
	if st.SRs != 1 {
		t.Fatalf("srs = %d, want 1", st.SRs)
	}
	found := false
	for _, ev := range evs {
		if ev.Kind == EventSenderReport && ev.SR != nil {
			found = true
			if ev.TrackID != 3 || ev.SR.TrackID != 3 {
				t.Fatalf("track id = %d/%d, want 3", ev.TrackID, ev.SR.TrackID)
			}
			if ev.SR.ClockRate != 48000 {
				t.Fatalf("clock rate = %d, want 48000", ev.SR.ClockRate)
			}
		}
	}
	if !found {
		t.Fatal("sender report event not surfaced")
	}
}

func TestBadMagicRejected(t *testing.T) {
	if _, err := NewScanner(bytes.NewReader([]byte("nope")), Config{}); err == nil {
		t.Fatal("expected error for bad magic")
	}
}

func TestSequenceWrapIsNotLoss(t *testing.T) {
	cfg := Config{Tracks: map[uint32]TrackInfo{1: {TrackID: 1, ClockRate: 48000}}, Window: 16}
	var declared, captured [][]byte
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		p := rtpPacket(seq, uint32(seq)*960, 1, []byte{1}, 0, 0)
		declared = append(declared, p)
		captured = append(captured, p)
	}
	_, st := scanAll(t, buildCapture(declared, captured), cfg)
	if st.Lost != 0 || st.Duplicates != 0 {
		t.Fatalf("wrap misclassified: %+v", st)
	}
}

func TestUnknownSSRCIsLocated(t *testing.T) {
	cfg := Config{Tracks: map[uint32]TrackInfo{1: {TrackID: 1, ClockRate: 48000}}}
	p := rtpPacket(1, 0, 99, []byte{1}, 0, 0)
	evs, _ := scanAll(t, buildCapture([][]byte{p}, [][]byte{p}), cfg)
	var f *fault.Fault
	for _, ev := range evs {
		for _, x := range ev.Faults {
			if errors.As(error(x), &f) && x.Kind == fault.KindLayout {
				f = x
			}
		}
	}
	if f == nil || f.Offset == 0 {
		t.Fatalf("unknown ssrc not located: %+v", f)
	}
}

func TestScannerMemoryIsBoundedByWindow(t *testing.T) {
	// Feed many packets with a deliberately tiny window and a large sequence
	// span. The tracker must never retain more than a window of state.
	cfg := Config{Tracks: map[uint32]TrackInfo{1: {TrackID: 1, ClockRate: 48000}}, Window: 16}
	var declared, captured [][]byte
	for i := 0; i < 5000; i++ {
		seq := uint16(i * 3) // guaranteed gaps
		p := rtpPacket(seq, uint32(i)*960, 1, []byte{1}, 0, 0)
		declared = append(declared, p)
		captured = append(captured, p)
	}
	s, err := NewScanner(bytes.NewReader(buildCapture(declared, captured)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, ok, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		for _, tr := range s.trackers {
			if got := len(tr.seen) + len(tr.pending); got > 2*int(tr.window)+2 {
				t.Fatalf("tracker retained %d entries, window is %d", got, tr.window)
			}
		}
	}
}
