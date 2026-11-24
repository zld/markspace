package fixture

import (
	"testing"
	"time"
)

func TestGenerateDeterministic(t *testing.T) {
	cfg := DefaultAudioConfig(2 * time.Second)
	plan := Plan{Seed: 42, LossBurst: 5, Reorder: 3}
	a, err := Generate(cfg, plan)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(cfg, plan)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.WAV) != string(b.WAV) {
		t.Fatal("WAV bytes differ across identical generations")
	}
	if len(a.SentSeqs) != len(b.SentSeqs) {
		t.Fatal("emitted sequence sets differ")
	}
	for i := range a.SentSeqs {
		if a.SentSeqs[i] != b.SentSeqs[i] {
			t.Fatalf("emitted order differs at %d", i)
		}
	}
}

func TestLossBurstRemovesPackets(t *testing.T) {
	cfg := DefaultAudioConfig(2 * time.Second) // 200 packets
	s, err := Generate(cfg, Plan{Seed: 1, LossBurst: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.LostSeqs) != 7 {
		t.Fatalf("lost=%d want 7", len(s.LostSeqs))
	}
	if len(s.Packets) != 200-7 {
		t.Fatalf("packets after loss=%d want %d", len(s.Packets), 200-7)
	}
	// Lost sequences must be contiguous (one burst).
	for i := 1; i < len(s.LostSeqs); i++ {
		if s.LostSeqs[i] != s.LostSeqs[i-1]+1 {
			t.Fatalf("loss is not a contiguous burst: %v", s.LostSeqs)
		}
	}
}

func TestReorderPreservesSet(t *testing.T) {
	cfg := DefaultAudioConfig(2 * time.Second)
	s, err := Generate(cfg, Plan{Seed: 9, Reorder: 4})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint16]bool{}
	for _, p := range s.Packets {
		if seen[p.SequenceNumber] {
			t.Fatal("reordering introduced a duplicate")
		}
		seen[p.SequenceNumber] = true
	}
	if len(seen) != len(s.Packets) || len(s.Packets) != 200 {
		t.Fatalf("reordering changed the packet set: %d unique", len(seen))
	}
}

func TestTruncatedWAVDoesNotMutatePristine(t *testing.T) {
	cfg := DefaultAudioConfig(time.Second)
	s, err := Generate(cfg, Plan{Seed: 2, TruncateAudio: 31})
	if err != nil {
		t.Fatal(err)
	}
	trunc := s.TruncatedWAV()
	if len(trunc) != len(s.WAV)-31 {
		t.Fatalf("truncated length=%d want %d", len(trunc), len(s.WAV)-31)
	}
	if string(trunc) != string(s.WAV[:len(trunc)]) {
		t.Fatal("truncation must be an exact prefix of the pristine WAV")
	}
}

func TestRejectsBadGeometry(t *testing.T) {
	bad := DefaultAudioConfig(time.Second)
	bad.Channels = 9
	if _, err := Generate(bad, Plan{Seed: 1}); err == nil {
		t.Fatal("9-channel geometry must be rejected")
	}
}

func Test24Bit8ChannelGeometry(t *testing.T) {
	cfg := DefaultAudioConfig(time.Second)
	cfg.Channels, cfg.BitsPerSample = 8, 24
	cfg.ClockTicksPerPkt = 900
	s, err := Generate(cfg, Plan{Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := cfg.PacketCount() * cfg.PacketSamples * cfg.BlockAlign()
	if len(s.PCM) != want {
		t.Fatalf("pcm=%d want %d", len(s.PCM), want)
	}
}
