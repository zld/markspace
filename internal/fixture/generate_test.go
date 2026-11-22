package fixture

import (
	"bytes"
	"testing"

	"mediacore/index"
	"mediacore/ingest"
	"mediacore/media"
)

func TestDefaultMaterialShape(t *testing.T) {
	plan := DefaultPlan()
	frames, err := plan.BuildFrames(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != DefaultFrames {
		t.Fatalf("frames=%d want %d", len(frames), DefaultFrames)
	}
	data, err := EncodeFrames(frames)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultFrames * RecordSizeFor(DefaultOptions())
	if len(data) != want {
		t.Fatalf("stream=%d want %d", len(data), want)
	}
	// The whole sample must scan cleanly.
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !idx.Complete || idx.EntryCount != DefaultFrames {
		t.Fatalf("complete=%t entries=%d", idx.Complete, idx.EntryCount)
	}
	// Each metadata block is exactly MetaSize bytes so offsets are predictable.
	for _, e := range idx.Entries[:3] {
		if e.Length != RecordSizeFor(DefaultOptions()) {
			t.Fatalf("record length=%d", e.Length)
		}
	}
}

func TestDuplicatePairsShareContent(t *testing.T) {
	plan := DefaultPlan()
	frames, _ := plan.BuildFrames(DefaultOptions())
	for _, pair := range plan.DuplicatePairs {
		a, b := frames[pair[0]], frames[pair[1]]
		if !bytes.Equal(a.Payload, b.Payload) {
			t.Fatalf("pair %v payloads differ", pair)
		}
		if a.RTPTimestamp == b.RTPTimestamp {
			t.Fatalf("pair %v should have different timestamps", pair)
		}
	}
	// Distinct frames must not collide.
	if bytes.Equal(frames[0].Payload, frames[1].Payload) {
		t.Fatal("adjacent frames should differ")
	}
}

func TestPacketFaultsProduceDuplicatesGapsAndLate(t *testing.T) {
	plan := DefaultPlan()
	frames, _ := plan.BuildFrames(DefaultOptions())
	pk, err := ApplyPacketFaults(frames, PacketPayload, PacketFaults{
		DuplicateEvery: 50,
		SwapEvery:      7,
		BurstLossStart: 480,
		BurstLossCount: 30,
		LateCount:      2,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ingest.ProcessPacketStream(pk, ingest.DefaultReorderConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicates == 0 {
		t.Fatal("expected duplicate packets")
	}
	if res.MissingFrames() == 0 || len(res.Gaps) == 0 {
		t.Fatalf("expected gaps, got %d", res.MissingFrames())
	}
	if res.Late == 0 {
		t.Fatal("expected late packets")
	}
}

func TestWrapTimestampsCrossBoundary(t *testing.T) {
	plan := DefaultPlan()
	opts := DefaultOptions()
	opts.WrapTimestamps = true
	frames, _ := plan.BuildFrames(opts)
	data, _ := EncodeFrames(frames)
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	wrapped := 0
	prev := int64(-1)
	for _, e := range idx.Entries {
		if e.Wrapped {
			wrapped++
		}
		if e.Ticks < prev {
			t.Fatalf("non-monotonic ticks at seq %d", e.Seq)
		}
		prev = e.Ticks
	}
	if wrapped == 0 {
		t.Fatal("expected a wrap event")
	}
}

func TestDamageLocatesFrame(t *testing.T) {
	plan := DefaultPlan()
	frames, _ := plan.BuildFrames(DefaultOptions())
	clean, _ := EncodeFrames(frames)
	rs := RecordSizeFor(DefaultOptions())
	damaged, off := ApplyFrameDamage(clean, rs, Damage{ChecksumFrame: 200})
	if off != int64(199*rs) {
		t.Fatalf("offset=%d want %d", off, 199*rs)
	}
	_, err := index.Scan(bytes.NewReader(damaged), index.DefaultOptions())
	if err == nil {
		t.Fatal("expected checksum failure")
	}
	e, ok := media.AsError(err)
	if !ok || e.Category != media.CatChecksum || e.FrameOffset != off {
		t.Fatalf("error=%v", err)
	}
}

func TestTruncationAtTail(t *testing.T) {
	plan := DefaultPlan()
	frames, _ := plan.BuildFrames(DefaultOptions())
	clean, _ := EncodeFrames(frames)
	damaged, off := ApplyFrameDamage(clean, RecordSizeFor(DefaultOptions()), Damage{TruncateTail: 900})
	if off != int64(len(damaged)) {
		t.Fatalf("offset=%d want %d", off, len(damaged))
	}
	_, err := index.Scan(bytes.NewReader(damaged), index.DefaultOptions())
	if err == nil {
		t.Fatal("expected truncation failure")
	}
	if media.CategoryOf(err) != media.CatTruncatedFrame {
		t.Fatalf("category=%s", media.CategoryOf(err))
	}
}
