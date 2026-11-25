package segment

import (
	"testing"

	"mediad/internal/model"
)

// fakeTimeline returns scripted media times and corruption PIDs.
type fakeTimeline struct {
	times []int64
	pos   int
	spans []struct {
		start int64
		pid   uint16
	}
}

func (f *fakeTimeline) MediaTicks() int64 {
	t := f.times[f.pos]
	if f.pos < len(f.times)-1 {
		f.pos++
	}
	return t
}

func (f *fakeTimeline) AffectedPIDs(mediaTicks int64) []uint16 {
	var out []uint16
	for _, s := range f.spans {
		if s.start <= mediaTicks {
			out = append(out, s.pid)
		}
	}
	return out
}

func mkPacket(pid uint16, cc uint8, off int64) *model.Packet {
	var p model.Packet
	p.PID = pid
	p.Continuity = cc
	p.Offset = off
	p.HasPayload = true
	return &p
}

func TestCutterBoundariesAndDescriptors(t *testing.T) {
	base := model.TimeBase{SegmentTicks: 100}
	tl := &fakeTimeline{times: []int64{0, 50, 100, 150, 200, 250}}
	c := NewCutter("video-high", base, tl, 25)
	var closed []*Descriptor
	for i := int64(0); i < 6; i++ {
		d, err := c.AddPacket(mkPacket(0x200, uint8(i), i*188))
		if err != nil {
			t.Fatal(err)
		}
		if d != nil {
			closed = append(closed, d)
		}
	}
	if d := c.Flush(); d != nil {
		closed = append(closed, d)
	}
	if len(closed) != 3 {
		t.Fatalf("closed segments = %d, want 3: %+v", len(closed), closed)
	}
	for i, d := range closed {
		if d.Index != i || d.Representation != "video-high" {
			t.Fatalf("descriptor %d = %+v", i, d)
		}
		if d.PacketCount != 2 {
			t.Fatalf("segment %d packets = %d, want 2", i, d.PacketCount)
		}
		if len(d.SHA256) != 64 {
			t.Fatalf("segment %d missing sha256: %q", i, d.SHA256)
		}
		if d.EndOffset <= d.StartOffset {
			t.Fatalf("segment %d bad offsets %d..%d", i, d.StartOffset, d.EndOffset)
		}
	}
}

func TestCutterPropagatesCorruption(t *testing.T) {
	base := model.TimeBase{SegmentTicks: 100}
	tl := &fakeTimeline{
		times: []int64{0, 50, 100, 150},
		spans: []struct {
			start int64
			pid   uint16
		}{{start: 100, pid: 0x200}},
	}
	c := NewCutter("v", base, tl, 25)
	var firstClosed *Descriptor
	for i := int64(0); i < 4; i++ {
		d, err := c.AddPacket(mkPacket(0x200, uint8(i), i*188))
		if err != nil {
			t.Fatal(err)
		}
		if d != nil {
			firstClosed = d
		}
	}
	if firstClosed == nil || firstClosed.Index != 0 || len(firstClosed.Corrupt) != 0 {
		t.Fatalf("segment 0 must be present and clean, got %+v", firstClosed)
	}
	d := c.Flush()
	if d == nil || len(d.Corrupt) != 1 || d.Corrupt[0] != 0x200 {
		t.Fatalf("segment 1 must carry pid 0x200, got %+v", d)
	}
}

func TestAlignDriftWithinOnePacket(t *testing.T) {
	base := model.TimeBase{SegmentTicks: 1000}
	a := NewAlign(base, 100)
	// Two reps whose observed boundaries differ by at most one packet tick.
	a.Observe(0, "high", 0)
	a.Observe(0, "low", 80)
	a.Observe(1, "high", 1000)
	a.Observe(1, "low", 1090)
	if err := a.Validate(); err != nil {
		t.Fatalf("drift within tolerance must pass: %v", err)
	}
	if a.MaxDriftTicks() != 90 {
		t.Fatalf("max drift = %d, want 90", a.MaxDriftTicks())
	}
	// One more segment pushes drift beyond a packet duration.
	a.Observe(2, "high", 2000)
	a.Observe(2, "low", 2150)
	if err := a.Validate(); err == nil {
		t.Fatal("drift over one packet duration must fail validation")
	}
}

func TestManifestDeterministic(t *testing.T) {
	reps := []Representation{
		{ID: "low", BitrateBps: 100_000},
		{ID: "high", BitrateBps: 500_000},
	}
	d1 := &Descriptor{Representation: "high", Index: 0, SHA256: "aa"}
	d2 := &Descriptor{Representation: "low", Index: 0, SHA256: "bb"}
	cor := []CorruptionReport{{Kind: "sync", Offset: 376, PID: 0x200}}
	m1 := NewManifest(1000, reps, []*Descriptor{d1, d2}, 90, cor)
	m2 := NewManifest(1000, reps, []*Descriptor{d2, d1}, 90, cor)
	r1, err := m1.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := m2.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(r1) != string(r2) {
		t.Fatalf("manifest ordering not deterministic:\n%s\nvs\n%s", r1, r2)
	}
	s1, _ := m1.SHA256()
	s2, _ := m2.SHA256()
	if s1 != s2 {
		t.Fatalf("manifest sha differs: %s vs %s", s1, s2)
	}
	if m1.SegmentCount != 1 || len(m1.Representations) != 2 ||
		m1.Representations[0].ID != "high" || m1.MaxAlignDrift != 90 {
		t.Fatalf("manifest summary wrong: %+v", m1)
	}
}
