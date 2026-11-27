package timeline

import (
	"math"
	"testing"

	"github.com/pion/rtp"
)

func TestScaleToNS(t *testing.T) {
	if v, err := ScaleToNS(48000, 48000); err != nil || v != 1_000_000_000 {
		t.Fatalf("v=%d err=%v", v, err)
	}
	if _, err := ScaleToNS(1, 0); err == nil {
		t.Fatal("zero timescale must error")
	}
	if _, err := ScaleToNS(math.MaxInt64, 90000); err == nil {
		t.Fatal("overflow must error")
	}
}

func TestDriftEstimation(t *testing.T) {
	// Raw clock runs 50ppm fast with a fixed 2ms offset.
	anchors := make([]Anchor, 10)
	for i := range anchors {
		ref := int64(i+1) * 1_000_000_000
		raw := int64(float64(ref-2_000_000)/(1+50e-6) + 0.5)
		anchors[i] = Anchor{RawNS: raw, ReferenceNS: ref}
	}
	d, err := EstimateDrift(anchors)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(d.RatePPM-50) > 0.01 {
		t.Fatalf("rate ppm = %f, want ~50", d.RatePPM)
	}
	if d.MaxAbsResNS > 2_000_000 {
		t.Fatalf("residual too large: %d", d.MaxAbsResNS)
	}
	corr, err := d.Apply(anchors[5].RawNS)
	if err != nil || math.Abs(float64(corr-anchors[5].ReferenceNS)) > 2_000_000 {
		t.Fatalf("corrected=%d ref=%d err=%v", corr, anchors[5].ReferenceNS, err)
	}
}

func TestSeqAndTimestampWrap(t *testing.T) {
	var su UnwrapSeq
	seq := []uint16{65533, 65534, 65535, 0, 1, 2}
	want := []uint64{65533, 65534, 65535, 65536, 65537, 65538}
	for i, s := range seq {
		if got := su.Push(s); got != want[i] {
			t.Fatalf("seq %d -> %d, want %d", s, got, want[i])
		}
	}
	var tu UnwrapTS
	ts := []uint32{0xFFFFFFF0, 0xFFFFFFF8, 0x00000000, 0x00000008}
	twant := []uint64{0xFFFFFFF0, 0xFFFFFFF8, 0x100000000, 0x100000008}
	for i, x := range ts {
		if got := tu.Push(x); got != twant[i] {
			t.Fatalf("ts %x -> %d, want %d", x, got, twant[i])
		}
	}
}

func TestRTPReorderDuplicatesAndGaps(t *testing.T) {
	s, err := NewRTPStream(RTPConfig{SSRC: 42, ClockRate: 48000, WindowPackets: 4})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(n uint16) *rtp.Packet {
		p := &rtp.Packet{}
		p.Version = 2
		p.PayloadType = 96
		p.SequenceNumber = n
		p.Timestamp = uint32(n) * 480
		return p
	}
	// Standard jitter semantics: first packet primes the baseline and emits.
	out0, _ := s.Push(mk(0))
	if len(out0) != 1 || out0[0].SequenceNumber != 0 {
		t.Fatalf("priming emit failed: %+v", out0)
	}
	// In-window reorder: 2 arrives before 1 and stays buffered.
	if out, _ := s.Push(mk(2)); len(out) != 0 {
		t.Fatalf("gap-bridging packet emitted early: %d", len(out))
	}
	out1, _ := s.Push(mk(1))
	if len(out1) != 2 || out1[0].SequenceNumber != 1 || out1[1].SequenceNumber != 2 {
		t.Fatalf("reorder failed: %+v", out1)
	}
	// Duplicate of 1 is idempotent.
	dup, _ := s.Push(mk(1))
	if dup != nil {
		t.Fatal("duplicate was not idempotent")
	}
	// seq 3 is lost; advance far enough to close the window over the hole.
	for n := uint16(4); n <= 12; n++ {
		if _, err := s.Push(mk(n)); err != nil {
			t.Fatal(err)
		}
	}
	gaps := s.Gaps()
	if len(gaps) != 1 || gaps[0].FromSequence != 3 || gaps[0].ToSequence != 3 {
		t.Fatalf("gap detection failed: %+v", gaps)
	}
	if ns, err := s.TimestampNS(48000); err != nil || ns != 1_000_000_000 {
		t.Fatalf("ts conversion: %d %v", ns, err)
	}
}

func TestWatermarkBoundaryAndLateReplay(t *testing.T) {
	wm := NewWatermark()
	r := NewReplayer(wm)
	mkItem := func(ns int64, key string) Item {
		return Item{Kind: KindClientEvent, Source: "ev", NS: ns, Key: key}
	}
	r.Ingest(mkItem(100, "a"))
	r.Ingest(mkItem(300, "c"))
	committed := r.Advance(200) // boundary: only a commits
	if len(committed) != 1 || committed[0].Key != "a" {
		t.Fatalf("boundary commit wrong: %+v", committed)
	}
	if wm.WatermarkNS() != 200 {
		t.Fatalf("watermark = %d", wm.WatermarkNS())
	}
	// Item exactly on the watermark is NOT late.
	if late, _ := r.Ingest(mkItem(200, "edge")); late {
		t.Fatal("item at watermark treated as late")
	}
	// Late item below watermark triggers rollback recompute.
	late, from := r.Ingest(mkItem(150, "b-late"))
	if !late || from != 200 {
		t.Fatalf("late routing wrong: late=%v from=%d", late, from)
	}
	if wm.ReplayCount() != 1 {
		t.Fatalf("replays = %d", wm.ReplayCount())
	}
	r.Advance(400)
	got := wm.Committed()
	wantOrder := []string{"a", "b-late", "edge", "c"}
	if len(got) != len(wantOrder) {
		t.Fatalf("committed count = %d", len(got))
	}
	for i, key := range wantOrder {
		if got[i].Key != key {
			t.Fatalf("position %d = %s, want %s", i, got[i].Key, key)
		}
	}
}

func TestReplayDeterminismAndIdempotency(t *testing.T) {
	base := []Item{
		{Kind: KindClientEvent, Source: "ev", NS: 100, Key: "a", Digest: []byte{1}},
		{Kind: KindClientEvent, Source: "ev", NS: 200, Key: "b", Digest: []byte{2}},
		{Kind: KindClientEvent, Source: "ev", NS: 300, Key: "c", Digest: []byte{3}},
		{Kind: KindClientEvent, Source: "ev", NS: 250, Key: "d", Digest: []byte{4}}, // late
	}
	// Arrival order shuffled, late one ingested after the watermark passes.
	wm := NewWatermark()
	r := NewReplayer(wm)
	r.Ingest(base[0])
	r.Ingest(base[1])
	r.Ingest(base[2])
	r.Advance(350)
	late, _ := r.Ingest(base[3])
	if !late {
		t.Fatal("expected late")
	}
	s1 := wm.Summary(nil)

	canonical := ReplaySorted(base, nil)
	if s1.Digest != canonical.Digest {
		t.Fatalf("digest mismatch:\nlate=%s\nclean=%s", s1.Digest, canonical.Digest)
	}
	if s1.ItemCount != canonical.ItemCount {
		t.Fatalf("item count %d vs %d", s1.ItemCount, canonical.ItemCount)
	}
	// Replaying the same late item again must not change the digest.
	before := wm.Summary(nil).Digest
	wm.Replay(base[3])
	after := wm.Summary(nil).Digest
	if before != after {
		t.Fatal("repeated replay changed digest (not idempotent)")
	}
}
