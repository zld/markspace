package ts

import (
	"bytes"
	"testing"

	"medkernel/media"
)

func parseAll(t *testing.T, stream *bytes.Buffer) []Packet {
	t.Helper()
	p := NewParser(Size188)
	var out []Packet
	if err := p.Parse(stream, func(pk Packet) error {
		// Detach every slice from the shared parse buffer.
		cp := pk
		cp.Raw = append([]byte(nil), pk.Raw...)
		cp.Payload = append([]byte(nil), pk.Payload...)
		cp.Adaptation = append([]byte(nil), pk.Adaptation...)
		out = append(out, cp)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestContinuityDuplicateAndGap(t *testing.T) {
	var stream bytes.Buffer
	ccs := []uint8{0, 1, 1, 2, 4, 5} // duplicate CC=1, then gap (CC 3 missing)
	for _, cc := range ccs {
		stream.Write(BuildPacket(0x300, cc, false, []byte{0x01}, -1, false))
	}
	pkts := parseAll(t, &stream)
	c := NewContinuityChecker()
	var dupes, gaps int
	for _, pk := range pkts {
		ev, bad := c.Check(pk)
		if !bad {
			continue
		}
		if ev.Duplicate {
			dupes++
			if ev.ByteEnd-ev.ByteOff != 188 {
				t.Fatalf("duplicate event must carry byte range, got %+v", ev)
			}
		} else {
			gaps++
			if ev.Gap != 1 {
				t.Fatalf("gap size=%d want 1", ev.Gap)
			}
		}
	}
	if dupes != 1 || gaps != 1 || c.Stats.Duplicates != 1 || c.Stats.Gaps != 1 {
		t.Fatalf("dupes=%d gaps=%d stats=%+v", dupes, gaps, c.Stats)
	}
	if c.Stats.Unrecovered != 1 {
		t.Fatalf("unflagged gap must be unrecovered, got %+v", c.Stats)
	}
}

func TestContinuityDiscontinuityFlag(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(BuildPacket(0x301, 0, false, []byte{1}, -1, false))
	stream.Write(BuildPacket(0x301, 1, false, []byte{2}, -1, false))
	// CC jumps 1 -> 5 but the adaptation field declares a discontinuity.
	stream.Write(BuildPacket(0x301, 5, false, []byte{3}, -1, true))
	pkts := parseAll(t, &stream)
	c := NewContinuityChecker()
	var events []ContinuityEvent
	for _, pk := range pkts {
		if ev, bad := c.Check(pk); bad {
			events = append(events, ev)
		}
	}
	if len(events) != 1 || events[0].Gap != 3 {
		t.Fatalf("want one 3-step gap event, got %+v", events)
	}
	if c.Stats.Recovered != 1 || c.Stats.Unrecovered != 0 {
		t.Fatalf("signalled discontinuity must count recovered: %+v", c.Stats)
	}
	if media.ClassOf(nil) != "" {
		t.Fatal("sanity")
	}
}

func TestPSIVersionBoundary(t *testing.T) {
	metaV1 := TrackMeta{Channels: 2, Layout: 2, SampleRate: 48000}
	metaV2 := TrackMeta{Channels: 6, Layout: 6, SampleRate: 44100}
	var stream bytes.Buffer
	stream.Write(BuildPacket(PIDPAT, 0, true, BuildPAT(0x1000, 0), -1, false))
	stream.Write(BuildPacket(0x1000, 0, true,
		BuildPMT(0x1000, 0x1100, 0x1200, 0, metaV1, "bt709"), -1, false))
	// Version bump: new channel layout and color tag.
	stream.Write(BuildPacket(PIDPAT, 1, true, BuildPAT(0x1000, 1), -1, false))
	stream.Write(BuildPacket(0x1000, 1, true,
		BuildPMT(0x1000, 0x1100, 0x1200, 1, metaV2, "bt2020"), -1, false))
	pkts := parseAll(t, &stream)

	w := NewPSIWatcher()
	for _, pk := range pkts {
		if err := w.Feed(pk); err != nil {
			t.Fatal(err)
		}
	}
	cur := w.Current()
	if cur.Version != 1 {
		t.Fatalf("current version=%d want 1", cur.Version)
	}
	audio := cur.Audio[0x1100]
	if audio.Channels != 6 || audio.SampleRate != 44100 {
		t.Fatalf("new generation metadata not applied: %+v", audio)
	}
	video := cur.Video[0x1200]
	if video.ColorTag != "bt2020" {
		t.Fatalf("color metadata wrong: %+v", video)
	}
	if len(w.Boundaries) != 2 {
		t.Fatalf("PAT+PMT must create two boundaries, got %d", len(w.Boundaries))
	}
	b := w.Boundaries[1]
	if b.PID != 0x1000 || b.OldVersion != 0 || b.NewVersion != 1 || b.Index != 3 {
		t.Fatalf("PMT boundary details wrong: %+v", b)
	}
}

func TestPSISectionReassemblyAcrossPackets(t *testing.T) {
	// Feed the watcher directly with payload-sized packets (the TS parser's
	// fixed 188-byte framing is exercised elsewhere); this isolates the
	// section reassembly across a PUSI head and a continuation tail.
	meta := TrackMeta{Channels: 2, Layout: 2, SampleRate: 48000}
	section := BuildPMT(0x1000, 0x1100, 0x1200, 0, meta, "bt709")
	split := 12 // head holds pointer_field + first section bytes only
	head := section[:split]
	tail := section[split:]

	w := NewPSIWatcher()
	// Register the PMT PID via a PAT first, exactly as a real stream does.
	if err := w.Feed(Packet{PID: PIDPAT, PUSI: true, HasPayload: true,
		Index: 0, Payload: BuildPAT(0x1000, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Feed(Packet{PID: 0x1000, PUSI: true, HasPayload: true,
		Index: 1, Payload: head}); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Current().Audio[0x1100]; ok {
		t.Fatal("section must not parse before the continuation arrives")
	}
	if err := w.Feed(Packet{PID: 0x1000, PUSI: false, HasPayload: true,
		Index: 2, Payload: tail}); err != nil {
		t.Fatal(err)
	}
	audio, ok := w.Current().Audio[0x1100]
	if !ok || audio.SampleRate != 48000 || audio.Channels != 2 {
		t.Fatalf("reassembled PMT metadata wrong: %+v ok=%v", audio, ok)
	}
	if len(w.Boundaries) != 0 {
		t.Fatalf("single-version PMT must not open a boundary, got %d", len(w.Boundaries))
	}
}
