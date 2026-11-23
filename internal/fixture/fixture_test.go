package fixture

import (
	"testing"

	"mediaroute/internal/codec"
)

func demoConfig() Config {
	return Config{
		Seed:          41,
		LossBurst:     3,
		ReorderWindow: 8,
		Duplicates:    4,
		Reorders:      6,
		Tracks: []TrackSpec{
			{SSRC: 10, Codec: codec.H264, Frames: 10, KeyEvery: 2,
				SeqStart: 65000, TSStart: 4_000_000_000, TSIncr: 3003},
			{SSRC: 20, Codec: codec.H265, Frames: 10, KeyEvery: 3,
				SeqStart: 1000, TSStart: 12345, TSIncr: 3003},
			{SSRC: 30, Codec: codec.AV1, Frames: 10, KeyEvery: 4,
				SeqStart: 500, TSStart: 0xFFFFFF00, TSIncr: 3003, TSWrap: true},
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
	if len(fx.BurstSeq) != 3 {
		t.Fatalf("each track needs one burst: %+v", fx.BurstSeq)
	}
	for ssrc, span := range fx.BurstSeq {
		if int(span[1]-span[0])+1 != 3 {
			t.Fatalf("track %d burst span wrong: %v", ssrc, span)
		}
	}
	// Truth = 3 tracks * 10 frames * 2 packets = 60; loss removes 9;
	// duplicates re-add 4 -> on-wire = 55.
	if fx.TruthPackets != 60 {
		t.Fatalf("truth packet count = %d want 60", fx.TruthPackets)
	}
	want := fx.TruthPackets - 3*3 + 4
	if len(fx.Packets) != want {
		t.Fatalf("on-wire packets = %d want %d", len(fx.Packets), want)
	}
	// Every track must have at least one incomplete (truncated) frame.
	for ssrc, frames := range fx.Frames {
		complete := 0
		for _, fr := range frames {
			if fr.Complete {
				complete++
			}
		}
		if complete == len(frames) {
			t.Fatalf("track %d shows no truncated frame", ssrc)
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
