package repair_test

import (
	"testing"

	"media-archiver/internal/media"
	"media-archiver/internal/repair"
)

func nsForFrame(f int64) int64 {
	return (media.Timebase{SampleRate: 48000}).FramesToNS(f)
}

func rawBlock(i int, frames int64, present bool, frameTS int64) media.RawBlock {
	return media.RawBlock{
		Index: i, DataOffset: int64(100 + i*100), CompleteFrames: frames,
		TimestampPresent: present, TimestampNS: nsForFrame(frameTS),
	}
}

func TestRepairTimelinePolicies(t *testing.T) {
	tb := media.Timebase{SampleRate: 48000}
	policy := repair.DefaultPolicy()

	t.Run("missing continues previous end", func(t *testing.T) {
		raw := []media.RawBlock{rawBlock(0, 240, true, 0), rawBlock(1, 240, false, 0)}
		res, err := repair.RepairTimeline(raw, tb, policy)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Decisions) != 1 || res.Decisions[0].Kind != "missing_timestamp" {
			t.Fatalf("decisions=%+v", res.Decisions)
		}
		if res.Blocks[1].StartFrame != 240 || res.Blocks[1].EndFrame != 480 {
			t.Fatalf("block timeline=%d..%d", res.Blocks[1].StartFrame, res.Blocks[1].EndFrame)
		}
	})

	t.Run("duplicate is replaced with continuity", func(t *testing.T) {
		raw := []media.RawBlock{rawBlock(0, 240, true, 0), rawBlock(1, 240, true, 0)}
		res, err := repair.RepairTimeline(raw, tb, policy)
		if err != nil {
			t.Fatal(err)
		}
		d := res.Decisions[0]
		if d.Kind != "duplicate_timestamp" || d.RepairedStartFrame != 240 {
			t.Fatalf("decision=%+v", d)
		}
		if d.OriginalStartNS == nil || *d.OriginalStartNS != 0 {
			t.Fatalf("original value not recorded: %+v", d)
		}
	})

	t.Run("backward is replaced and audited", func(t *testing.T) {
		raw := []media.RawBlock{rawBlock(0, 240, true, 0), rawBlock(1, 240, true, 120)}
		res, err := repair.RepairTimeline(raw, tb, policy)
		if err != nil {
			t.Fatal(err)
		}
		d := res.Decisions[0]
		if d.Kind != "backward_timestamp" {
			t.Fatalf("kind=%s", d.Kind)
		}
		if d.OriginalStartFrame == nil || *d.OriginalStartFrame != 120 || d.RepairedStartFrame != 240 {
			t.Fatalf("unexpected old/new frames: %+v", d)
		}
	})

	t.Run("excess jump is repaired", func(t *testing.T) {
		raw := []media.RawBlock{rawBlock(0, 240, true, 0), rawBlock(1, 240, true, 1000)}
		res, err := repair.RepairTimeline(raw, tb, policy)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Decisions[0].Kind; got != "excess_jump_timestamp" {
			t.Fatalf("kind=%s", got)
		}
		if res.Blocks[1].StartFrame != 240 {
			t.Fatalf("start=%d want240", res.Blocks[1].StartFrame)
		}
	})
}

func TestAssembleHashesOnlyCompleteFramesAndProvenance(t *testing.T) {
	format := media.PCMFormat{
		AudioFormat: 1, Channels: 2, SampleRate: 48000,
		ByteRate: 192000, BlockAlign: 4, BitsPerSample: 16,
	}
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0} // two stereo frames
	raw := []media.RawBlock{{
		Index: 0, DataOffset: 68, CompleteFrames: 2, Data: append(append([]byte{}, pcm...), 0x99),
		TimestampPresent: true, TimestampNS: 0, Truncated: true, CorruptOffset: 76,
	}}
	res, err := repair.Assemble(format, raw, media.Timebase{SampleRate: 48000}, repair.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks[0].PCM) != 8 {
		t.Fatalf("pcm len=%d, partial tail must not be padded/included", len(res.Blocks[0].PCM))
	}
	if res.Blocks[0].AudioSHA256 != media.SHA256Hex(pcm) {
		t.Fatal("block digest does not match complete-frame PCM")
	}
	if repair.CountBadBlocks(res.Blocks) != 1 {
		t.Fatal("bad block not counted")
	}
	prov := repair.NewProvenance(repair.InputRecord{SHA256: "abc"}, repair.DefaultPolicy(), res)
	data, err := prov.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || prov.Input.SHA256 != "abc" || len(prov.Blocks) != 1 {
		t.Fatalf("provenance incomplete: %+v", prov)
	}
}
