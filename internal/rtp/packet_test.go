package rtp

import (
	"testing"

	"regional.example/mediad/internal/clock"
)

func buildTimeline(t *testing.T, start int64, media, gap, media2 int64) *clock.Timeline {
	t.Helper()
	tl, err := clock.NewTimeline(clock.Rate48000)
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.SetStart(start); err != nil {
		t.Fatal(err)
	}
	if media > 0 {
		if _, err := tl.AddMedia("a", 0, media); err != nil {
			t.Fatal(err)
		}
	}
	if gap > 0 {
		if _, err := tl.AddGap(gap); err != nil {
			t.Fatal(err)
		}
	}
	if media2 > 0 {
		if _, err := tl.AddMedia("a", media, media2); err != nil {
			t.Fatal(err)
		}
	}
	return tl
}

func TestGenerateMonotonic(t *testing.T) {
	tl := buildTimeline(t, 0, 48000, 0, 0)
	s, err := Generate(tl, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Packets) != 50 { // 48000/960
		t.Fatalf("packets %d", len(s.Packets))
	}
	if s.NonMonotonic != 0 {
		t.Fatalf("non-monotonic %d", s.NonMonotonic)
	}
	if err := s.ValidateTimestamps(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateMarksGap(t *testing.T) {
	tl := buildTimeline(t, 0, 48000, 4800, 48000)
	s, err := Generate(tl, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if s.GapsBefore != 1 {
		t.Fatalf("gaps %d", s.GapsBefore)
	}
	if s.MissingTicks != 4800 {
		t.Fatalf("missing %d", s.MissingTicks)
	}
	if err := s.ValidateTimestamps(); err != nil {
		t.Fatal(err)
	}
	// The packet after the gap must carry Marker and GapBefore.
	found := false
	for _, p := range s.Packets {
		if p.GapBefore {
			found = true
			if !p.Marker {
				t.Fatal("gap packet lacks marker")
			}
		}
	}
	if !found {
		t.Fatal("no packet marks the gap")
	}
}

func TestGenerateBitrateInRange(t *testing.T) {
	tl := buildTimeline(t, 0, 48000, 0, 0)
	cfg := DefaultConfig()
	s, _ := Generate(tl, cfg)
	target, actual, dev := s.KPSSummary(128)
	if target != 128 {
		t.Fatalf("target %v", target)
	}
	// 960 frames * 4 bytes * 8 bits / 0.02 s = 1536 kbit/s.
	if actual < 1500 || actual > 1600 {
		t.Fatalf("actual %.1f", actual)
	}
	if dev <= 0 {
		t.Fatal("deviation should be positive vs 128 target")
	}
}

func TestGenerateNonZeroTimestampBase(t *testing.T) {
	tl := buildTimeline(t, 96000, 48000, 0, 0)
	cfg := DefaultConfig()
	cfg.TimestampBase = 12345
	s, _ := Generate(tl, cfg)
	// The first packet's timestamp is its presentation position folded into
	// the RTP clock plus the configured base offset.
	if want := uint32(96000) + 12345; s.Packets[0].Timestamp != want {
		t.Fatalf("first ts %d want %d", s.Packets[0].Timestamp, want)
	}
	if s.Packets[0].StartTick != 96000 {
		t.Fatalf("start tick %d", s.Packets[0].StartTick)
	}
}

func TestGenerateSequenceWrap(t *testing.T) {
	tl := buildTimeline(t, 0, 48000*3, 0, 0)
	cfg := DefaultConfig()
	cfg.StartSeq = 65500
	s, _ := Generate(tl, cfg)
	// 150 packets, so the sequence wraps.
	wrapped := false
	for i := 1; i < len(s.Packets); i++ {
		if s.Packets[i].SequenceNumber < s.Packets[i-1].SequenceNumber {
			wrapped = true
		}
	}
	if !wrapped {
		t.Fatal("sequence did not wrap")
	}
}

func TestGenerateOnlyMediaCounts(t *testing.T) {
	tl := buildTimeline(t, 0, 48000, 48000, 48000)
	s, _ := Generate(tl, DefaultConfig())
	if s.MediaTicks != 96000 {
		t.Fatalf("media ticks %d", s.MediaTicks)
	}
	if s.MissingTicks != 48000 {
		t.Fatalf("missing ticks %d", s.MissingTicks)
	}
	if s.TotalFrames != 96000 {
		t.Fatalf("frames %d", s.TotalFrames)
	}
}

func TestGenerateRejectsBadConfig(t *testing.T) {
	tl := buildTimeline(t, 0, 48000, 0, 0)
	cfg := DefaultConfig()
	cfg.FramesPerPacket = 0
	if _, err := Generate(tl, cfg); err == nil {
		t.Fatal("expected frames rejection")
	}
}

func TestRoundTripThroughPion(t *testing.T) {
	tl := buildTimeline(t, 0, 48000, 0, 0)
	s, _ := Generate(tl, DefaultConfig())
	decoded, err := s.ParsePackets()
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(s.Packets) {
		t.Fatalf("decoded %d", len(decoded))
	}
	if decoded[0].Header.PayloadType != s.Config.PayloadType {
		t.Fatal("payload type lost")
	}
}

func TestValidateTimestampsDetectsJump(t *testing.T) {
	s := &Summary{Packets: []PacketSummary{
		{SequenceNumber: 1, Timestamp: 0, Frames: 960},
		{SequenceNumber: 2, Timestamp: 5, Frames: 960},
	}}
	if err := s.ValidateTimestamps(); err == nil {
		t.Fatal("expected jump detection")
	}
}

func TestValidateTimestampsDetectsFakeGap(t *testing.T) {
	s := &Summary{Packets: []PacketSummary{
		{SequenceNumber: 1, Timestamp: 0, Frames: 960},
		{SequenceNumber: 2, Timestamp: 960, Frames: 960, GapBefore: true},
	}}
	if err := s.ValidateTimestamps(); err == nil {
		t.Fatal("expected fake-gap detection")
	}
}

func TestBitrateNoMedia(t *testing.T) {
	s := &Summary{}
	if s.BitrateKbps() != 0 {
		t.Fatal("empty bitrate")
	}
	if _, actual, dev := s.KPSSummary(128); actual != 0 || dev != 100 {
		t.Fatalf("actual %v dev %v", actual, dev)
	}
}
