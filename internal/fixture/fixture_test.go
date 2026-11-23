package fixture

import (
	"strconv"
	"testing"

	"mediaroute/internal/codec"
	"mediaroute/internal/transport"
)

func demoConfig() Config {
	return Config{
		Seed:          41,
		LossBurst:     3,
		ReorderWindow: 8,
		Duplicates:    4,
		Reorders:      6,
		Tracks: []TrackSpec{
			{SSRC: 10, Codec: codec.H264, Frames: 50, KeyEvery: 10,
				SeqStart: 65000, TSStart: 4_000_000_000, TSIncr: 3003,
				BurstEnabled: true},
			{SSRC: 20, Codec: codec.H265, Frames: 50, KeyEvery: 8,
				SeqStart: 1000, TSStart: 12345, TSIncr: 3003},
			{SSRC: 30, Codec: codec.AV1, Frames: 50, KeyEvery: 8,
				SeqStart: 500, TSStart: 0xFFFF0000, TSIncr: 3003, TSWrap: true},
		},
	}
}

func TestDeterministicSameSeed(t *testing.T) {
	a := Build(demoConfig())
	b := Build(demoConfig())
	if len(a.Packets) != len(b.Packets) || a.TruthPackets != b.TruthPackets {
		t.Fatal("same seed produced different stream size")
	}
	for i, pa := range a.Packets {
		pb := b.Packets[i]
		if pa.SSRC != pb.SSRC || pa.Seq != pb.Seq || pa.Timestamp != pb.Timestamp ||
			string(pa.Payload) != string(pb.Payload) {
			t.Fatalf("packet %d differs across same-seed builds", i)
		}
	}
}

func TestBurstLossAndTruncation(t *testing.T) {
	fx := Build(demoConfig())
	// Only the BurstEnabled track (SSRC 10) carries the single loss burst.
	if len(fx.BurstSeq) != 1 {
		t.Fatalf("exactly one track should carry the burst: %+v", fx.BurstSeq)
	}
	span, ok := fx.BurstSeq[10]
	if !ok || int(span[1]-span[0])+1 != 3 {
		t.Fatalf("burst span on track 10 wrong: %+v", fx.BurstSeq)
	}
	// Truth = 3 tracks * 50 frames * 2 packets = 300; loss removes 3;
	// duplicates re-add 4 -> on-wire = 301.
	if fx.TruthPackets != 300 {
		t.Fatalf("truth packet count = %d want 300", fx.TruthPackets)
	}
	want := fx.TruthPackets - 3 + 4
	if len(fx.Packets) != want {
		t.Fatalf("on-wire packets = %d want %d", len(fx.Packets), want)
	}
	// The burst-enabled track must have at least one incomplete frame while
	// the other tracks remain fully complete.
	for ssrc, frames := range fx.Frames {
		var incomplete int
		for _, fr := range frames {
			if !fr.Complete {
				incomplete++
			}
		}
		if ssrc == 10 && incomplete == 0 {
			t.Fatalf("burst track %d shows no truncated frame", ssrc)
		}
		if ssrc != 10 && incomplete != 0 {
			t.Fatalf("clean track %d unexpectedly truncated", ssrc)
		}
	}
}

func TestKeySamplesOnlyComplete(t *testing.T) {
	fx := Build(demoConfig())
	for ssrc, keys := range fx.KeySamples {
		for sample := range keys {
			fr := fx.Frames[ssrc][sample-1]
			if !fr.Complete || !fr.Keyframe {
				t.Fatalf("track %d sample %d indexed but not a complete keyframe", ssrc, sample)
			}
		}
	}
}

func TestTimestampWraparoundFixture(t *testing.T) {
	fx := Build(demoConfig())
	frames := fx.Frames[30]
	wrapped := false
	prev := uint32(0)
	first := true
	for _, fr := range frames {
		if !first && fr.Timestamp < prev {
			wrapped = true
		}
		prev = fr.Timestamp
		first = false
	}
	if !wrapped {
		t.Fatal("AV1 track did not exhibit timestamp wraparound")
	}
}

func TestNoImpairmentBaseline(t *testing.T) {
	cfg := demoConfig()
	cfg.LossBurst, cfg.Duplicates, cfg.Reorders = 0, 0, 0
	fx := Build(cfg)
	if len(fx.Packets) != fx.TruthPackets {
		t.Fatal("clean build must keep every packet")
	}
	for _, frames := range fx.Frames {
		for _, fr := range frames {
			if !fr.Complete {
				t.Fatal("clean build must produce only complete frames")
			}
		}
	}
}

func TestBandwidthModelReordersWithoutLosing(t *testing.T) {
	// Isolate the link model: duplicate/reorder injection draws its targets
	// from arrival positions, so it must be off for a multiset comparison.
	base := demoConfig()
	base.Duplicates, base.Reorders = 0, 0
	cfg := base
	cfg.Bandwidth = true
	cfg.BandwidthDepth = 6
	fx := Build(cfg)
	if fx.CongestionPeak < 1 || fx.CongestionPeak > 6 {
		t.Fatalf("congestion peak = %d want 1..6", fx.CongestionPeak)
	}
	// The link model only re-sequences arrival; it never drops or duplicates,
	// so the on-wire multiset must match a plain build exactly.
	plain := Build(base)
	if len(fx.Packets) != len(plain.Packets) {
		t.Fatalf("bandwidth model changed packet count: %d != %d",
			len(fx.Packets), len(plain.Packets))
	}
	if fx.TruthPackets != plain.TruthPackets {
		t.Fatalf("bandwidth model changed truth count")
	}
	key := func(p *transport.Packet) string {
		return strconv.FormatUint(uint64(p.SSRC), 10) + ":" +
			strconv.FormatUint(uint64(p.Seq), 10)
	}
	multiset := func(pkts []*transport.Packet) map[string]int {
		m := map[string]int{}
		for _, p := range pkts {
			m[key(p)]++
		}
		return m
	}
	a, b := multiset(fx.Packets), multiset(plain.Packets)
	if len(a) != len(b) {
		t.Fatalf("distinct packet sets differ: %d != %d", len(a), len(b))
	}
	for k, n := range b {
		if a[k] != n {
			t.Fatalf("packet %s multiplicity %d != %d", k, a[k], n)
		}
	}
	// Arrival order must actually have changed.
	same := true
	for i := range fx.Packets {
		if key(fx.Packets[i]) != key(plain.Packets[i]) {
			same = false
			break
		}
	}
	if same {
		t.Fatal("bandwidth model left arrival order untouched")
	}
}

func TestBandwidthModelDeterministic(t *testing.T) {
	cfg := demoConfig()
	cfg.Bandwidth = true
	cfg.BandwidthDepth = 4
	a := Build(cfg)
	b := Build(cfg)
	for i := range a.Packets {
		if a.Packets[i].Seq != b.Packets[i].Seq ||
			a.Packets[i].SSRC != b.Packets[i].SSRC {
			t.Fatalf("bandwidth model not deterministic at packet %d", i)
		}
	}
	if a.CongestionPeak != b.CongestionPeak {
		t.Fatal("congestion peak not deterministic")
	}
}
