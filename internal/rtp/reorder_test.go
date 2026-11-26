package rtp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"rtx/internal/model"
)

func mustPacket(t *testing.T, src string, seq uint16, ts uint32) Packet {
	t.Helper()
	p, err := BuildPacket(src, 0x11223344, 96, seq, ts, false, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildPacketParses(t *testing.T) {
	p := mustPacket(t, "a", 7, 9000)
	tr := &Trace{Packets: []Packet{p}}
	got, err := Reorder(tr)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Packets[0].Seq != 7 || got[0].Packets[0].TS != 9000 {
		t.Fatalf("round trip lost header: %+v", got[0].Packets[0])
	}
	payload, err := got[0].Packets[0].PayloadBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte{1, 2, 3}) {
		t.Fatalf("payload = %x", payload)
	}
}

func TestReorderOutOfOrder(t *testing.T) {
	// Recorded order 10,12,11,13 must restore to 10,11,12,13.
	tr := &Trace{Packets: []Packet{
		mustPacket(t, "a", 10, 0),
		mustPacket(t, "a", 12, 0),
		mustPacket(t, "a", 11, 0),
		mustPacket(t, "a", 13, 0),
	}}
	streams, err := Reorder(tr)
	if err != nil {
		t.Fatal(err)
	}
	s := streams[0]
	var seqs []uint16
	for _, p := range s.Packets {
		seqs = append(seqs, p.Seq)
	}
	if want := []uint16{10, 11, 12, 13}; !equalSeqs(seqs, want) {
		t.Fatalf("seqs = %v, want %v", seqs, want)
	}
	if s.OutOfOrder != 1 {
		t.Fatalf("outOfOrder = %d, want 1", s.OutOfOrder)
	}
	if len(s.Gaps) != 0 {
		t.Fatalf("gaps = %v, want none", s.Gaps)
	}
}

func TestReorderLossRecordsGap(t *testing.T) {
	// 21 and 22 are lost; no media duration is invented for them.
	tr := &Trace{Packets: []Packet{
		mustPacket(t, "a", 20, 0),
		mustPacket(t, "a", 23, 0),
		mustPacket(t, "a", 24, 0),
	}}
	streams, err := Reorder(tr)
	if err != nil {
		t.Fatal(err)
	}
	s := streams[0]
	if len(s.Packets) != 3 {
		t.Fatalf("packets = %d, want 3", len(s.Packets))
	}
	want := []int64{21, 22}
	if len(s.Gaps) != len(want) || s.Gaps[0] != want[0] || s.Gaps[1] != want[1] {
		t.Fatalf("gaps = %v, want %v", s.Gaps, want)
	}
}

func TestReorderSequenceWrap(t *testing.T) {
	// Cross the 16-bit boundary in recorded order with a small reorder.
	tr := &Trace{Packets: []Packet{
		mustPacket(t, "a", 65534, 4294967000),
		mustPacket(t, "a", 0, 4294967290),
		mustPacket(t, "a", 65535, 4294967010),
		mustPacket(t, "a", 1, 100),
	}}
	streams, err := Reorder(tr)
	if err != nil {
		t.Fatal(err)
	}
	s := streams[0]
	seqs := []uint16{s.Packets[0].Seq, s.Packets[1].Seq, s.Packets[2].Seq, s.Packets[3].Seq}
	if want := []uint16{65534, 65535, 0, 1}; !equalSeqs(seqs, want) {
		t.Fatalf("seqs = %v, want %v", seqs, want)
	}
	if s.WrapCount() != 1 {
		t.Fatalf("wrap count = %d, want 1", s.WrapCount())
	}
	if len(s.Gaps) != 0 {
		t.Fatalf("gaps = %v", s.Gaps)
	}
}

func TestReorderDuplicateDrops(t *testing.T) {
	tr := &Trace{Packets: []Packet{
		mustPacket(t, "a", 5, 100),
		mustPacket(t, "a", 5, 100),
		mustPacket(t, "a", 6, 200),
	}}
	streams, err := Reorder(tr)
	if err != nil {
		t.Fatal(err)
	}
	if len(streams[0].Packets) != 2 || streams[0].Duplicates != 1 {
		t.Fatalf("packets=%d duplicates=%d", len(streams[0].Packets), streams[0].Duplicates)
	}
}

func TestReorderAmbiguousHalfSpace(t *testing.T) {
	tr := &Trace{Packets: []Packet{
		mustPacket(t, "a", 0, 0),
		mustPacket(t, "a", 32768, 100),
	}}
	if _, err := Reorder(tr); err == nil {
		t.Fatal("expected ambiguous sequence error")
	} else {
		e, ok := model.As(err)
		if !ok || e.Code != CodeRTPSequence {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestAnchorExactMapping(t *testing.T) {
	r90, _ := model.NewRate(90000)
	r48, _ := model.NewRate(48000)
	media, err := model.UnifyRate(r90, r48)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMapping(media, []Anchor{
		{Source: "video", RTPTimestamp: 1000, Wrap: 0, MediaTicks: 0, ClockRate: 90000},
		{Source: "audio", RTPTimestamp: 480, Wrap: 0, MediaTicks: 0, ClockRate: 48000},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := mustPacket(t, "video", 1, 1900) // +900 ticks = +10ms
	got, err := m.MapPacket(p)
	if err != nil {
		t.Fatal(err)
	}
	// 10ms at 720000 = 7200 ticks.
	if got.Ticks != 7200 {
		t.Fatalf("mapped ticks = %d, want 7200", got.Ticks)
	}
	pa := mustPacket(t, "audio", 1, 960) // +480 ticks = +10ms
	ga, err := m.MapPacket(pa)
	if err != nil {
		t.Fatal(err)
	}
	if ga.Ticks != 7200 {
		t.Fatalf("audio ticks = %d, want 7200", ga.Ticks)
	}
}

func TestAnchorMissingSourceFails(t *testing.T) {
	media, _ := model.NewRate(720000)
	m, err := NewMapping(media, []Anchor{{Source: "a", RTPTimestamp: 0, Wrap: 0, MediaTicks: 0, ClockRate: 90000}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.MapPacket(mustPacket(t, "b", 1, 0)); err == nil {
		t.Fatal("expected error for unanchored source")
	}
}

func TestAnchorConflictFails(t *testing.T) {
	media, _ := model.NewRate(720000)
	_, err := NewMapping(media, []Anchor{
		{Source: "a", RTPTimestamp: 0, Wrap: 0, MediaTicks: 0, ClockRate: 90000},
		{Source: "a", RTPTimestamp: 0, Wrap: 0, MediaTicks: 90000, ClockRate: 90000},
	})
	if err == nil {
		t.Fatal("expected conflicting anchors to fail")
	}
	if e, _ := model.As(err); e == nil || e.Code != model.CodeAnchorMismatch {
		t.Fatalf("error = %v", err)
	}
}

func TestAnchorWrapsTimestamp32(t *testing.T) {
	r90, _ := model.NewRate(90000)
	r48, _ := model.NewRate(48000)
	media, _ := model.UnifyRate(r90, r48)
	// Anchor just before the 32-bit rollover; a later packet has a small TS.
	m, err := NewMapping(media, []Anchor{
		{Source: "v", RTPTimestamp: 4294967266, Wrap: 0, MediaTicks: 0, ClockRate: 90000},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The packet carries a tiny timestamp after the 32-bit rollover; it is
	// 40 ticks past the anchor once unwrapped, so 40/90000 s = 320 media ticks.
	p := mustPacket(t, "v", 2, 10)
	got, err := m.MapPacket(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 320 {
		t.Fatalf("ticks = %d, want 320", got.Ticks)
	}
}

func TestAnchorInexactFails(t *testing.T) {
	r90, _ := model.NewRate(90000)
	r48, _ := model.NewRate(48000)
	media, _ := model.UnifyRate(r90, r48)
	m, err := NewMapping(media, []Anchor{{Source: "a", RTPTimestamp: 0, Wrap: 0, MediaTicks: 0, ClockRate: 44100}})
	if err != nil {
		t.Fatal(err)
	}
	// 1 tick at 44.1kHz does not land on the 720000 grid exactly.
	if _, err := m.MapPacket(mustPacket(t, "a", 1, 1)); err == nil {
		t.Fatal("expected inexact mapping error")
	}
}

func TestSourceSwitchDetected(t *testing.T) {
	r90, _ := model.NewRate(90000)
	media, _ := model.UnifyRate(r90)
	m, err := NewMapping(media, []Anchor{
		{Source: "cam-a", RTPTimestamp: 0, Wrap: 0, MediaTicks: 0, ClockRate: 90000},
		{Source: "cam-b", RTPTimestamp: 500000, Wrap: 0, MediaTicks: 14400000, ClockRate: 90000},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Stream{Source: "cam-a", Packets: []Packet{mustPacket(t, "cam-a", 1, 0)}}
	b := &Stream{Source: "cam-b", Packets: []Packet{mustPacket(t, "cam-b", 1, 500000)}}
	sw, err := m.DetectSwitches([]*Stream{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(sw) != 1 || sw[0].From != "cam-a" || sw[0].To != "cam-b" {
		t.Fatalf("switches = %+v", sw)
	}
}

func TestTraceLoadRejectsTamperedDeclaration(t *testing.T) {
	dir := t.TempDir()
	pkts := []Packet{mustPacket(t, "a", 1, 100)}
	path := filepath.Join(dir, "t.jsonl")
	if err := WriteTraceFile(dir, "t.jsonl", pkts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the declared sequence without touching the datagram.
	bad := bytes.Replace(data, []byte(`"seq":1`), []byte(`"seq":2`), 1)
	if bytes.Equal(bad, data) {
		t.Fatal("test setup: replacement did not apply")
	}
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadTrace(path)
	if err == nil {
		t.Fatal("expected tamper error")
	}
	if e, _ := model.As(err); e == nil || e.Code != CodeRTPTrace {
		t.Fatalf("error = %v", err)
	}
}

func TestTraceLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pkts := []Packet{mustPacket(t, "a", 1, 100), mustPacket(t, "a", 2, 200)}
	if err := WriteTraceFile(dir, "t.jsonl", pkts); err != nil {
		t.Fatal(err)
	}
	tr, err := LoadTrace(filepath.Join(dir, "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Packets) != 2 || tr.Packets[1].TS != 200 {
		t.Fatalf("loaded = %+v", tr.Packets)
	}
}

func equalSeqs(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAnchorAfter32BitWrapSecondCycle(t *testing.T) {
	// The anchor itself is in the second 32-bit cycle (wrap=1). Using only the
	// anchor's low 32 bits would map the whole source to the wrong cycle.
	r90, _ := model.NewRate(90000)
	r48, _ := model.NewRate(48000)
	media, _ := model.UnifyRate(r90, r48)
	m, err := NewMapping(media, []Anchor{{Source: "v", RTPTimestamp: 0, Wrap: 1, MediaTicks: 1_000_000, ClockRate: 90000}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.MapPacket(mustPacket(t, "v", 1, 90000))
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(1_000_000) + 720000; got.Ticks != want {
		t.Fatalf("ticks = %d, want %d", got.Ticks, want)
	}
}

func TestAnchorSecondCycleLowTimestamp(t *testing.T) {
	// wrap=1 with a low timestamp: the extended value is 2^32+ts, not ts.
	r90, _ := model.NewRate(90000)
	media, _ := model.UnifyRate(r90)
	m, err := NewMapping(media, []Anchor{{Source: "v", RTPTimestamp: 0, Wrap: 1, MediaTicks: 0, ClockRate: 90000}})
	if err != nil {
		t.Fatal(err)
	}
	// 1 second at 90 kHz clock => 90000 media ticks on the 90000 grid.
	got, err := m.MapPacket(mustPacket(t, "v", 1, 90000))
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 90000 {
		t.Fatalf("ticks = %d, want 90000", got.Ticks)
	}
}

func TestReorderSequenceWrapMidStream(t *testing.T) {
	tr := &Trace{Packets: []Packet{
		mustPacket(t, "a", 65533, 0), mustPacket(t, "a", 65534, 0),
		mustPacket(t, "a", 65535, 0), mustPacket(t, "a", 0, 0),
		mustPacket(t, "a", 1, 0), mustPacket(t, "a", 2, 0),
	}}
	streams, err := Reorder(tr)
	if err != nil {
		t.Fatal(err)
	}
	s := streams[0]
	var seqs []uint16
	for _, p := range s.Packets {
		seqs = append(seqs, p.Seq)
	}
	if want := []uint16{65533, 65534, 65535, 0, 1, 2}; !equalSeqs(seqs, want) {
		t.Fatalf("seqs = %v, want %v", seqs, want)
	}
	if s.Wraps != 1 || len(s.Gaps) != 0 {
		t.Fatalf("wraps=%d gaps=%v", s.Wraps, s.Gaps)
	}
}
