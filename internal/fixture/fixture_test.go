package fixture

import (
	"path/filepath"
	"testing"
)

func TestBuildAllKinds(t *testing.T) {
	kinds := []Kind{KindNormal, KindWrap, KindDrift, KindVFR, KindDupReorder, KindBurstLoss, KindTruncated}
	for _, k := range kinds {
		spec := DefaultSpec(k)
		if k == KindBurstLoss {
			spec.DropAt, spec.DropCount = 20, 3
		}
		pkts := Build(spec)
		if len(pkts) == 0 {
			t.Fatalf("%s produced no packets", k)
		}
		// Every generated packet must carry the configured SSRC.
		for _, p := range pkts {
			if p.SSRC != spec.SSRC {
				t.Fatalf("%s: unexpected SSRC %#x", k, p.SSRC)
			}
		}
	}
}

func TestWrapTimestampActuallyWraps(t *testing.T) {
	spec := DefaultSpec(KindWrap)
	pkts := Build(spec)
	sawWrap := false
	for i := 1; i < len(pkts); i++ {
		if pkts[i].Timestamp < pkts[i-1].Timestamp {
			sawWrap = true
		}
	}
	if !sawWrap {
		t.Fatal("wrap scenario never decreased its raw timestamp")
	}
}

func TestVFRHasNonUniformButForwardTimestamps(t *testing.T) {
	spec := DefaultSpec(KindVFR)
	pkts := Build(spec)
	var deltas []uint32
	for i := 1; i < len(pkts); i++ {
		d := pkts[i].Timestamp - pkts[i-1].Timestamp
		if int32(d) <= 0 {
			t.Fatalf("VFR timestamp went backwards at %d", i)
		}
		deltas = append(deltas, d)
	}
	uniform := true
	for _, d := range deltas {
		if d != deltas[0] {
			uniform = false
		}
	}
	if uniform {
		t.Fatal("VFR timestamps are uniform; scenario is not actually variable rate")
	}
}

func TestBurstLossRemovesPackets(t *testing.T) {
	spec := DefaultSpec(KindBurstLoss)
	spec.DropAt, spec.DropCount = 20, 3
	pkts := Build(spec)
	seqs := map[uint16]bool{}
	for _, p := range pkts {
		if seqs[p.SequenceNumber] {
			t.Fatal("duplicate seq in burst-loss fixture")
		}
		seqs[p.SequenceNumber] = true
	}
	for s := 20; s < 23; s++ {
		if seqs[uint16(s)] {
			t.Fatalf("dropped seq %d still present", s)
		}
	}
}

func TestWriteAndReadCaptureRoundTrip(t *testing.T) {
	spec := DefaultSpec(KindNormal)
	pkts := Build(spec)
	dir := t.TempDir()
	path, err := WriteCapture(filepath.Join(dir, "cap"), "n.rtp", pkts, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, off, err := ReadCapture(path)
	if err != nil || off != -1 {
		t.Fatalf("round trip: %v off=%d", err, off)
	}
	if len(got) != len(pkts) {
		t.Fatalf("read %d packets, wrote %d", len(got), len(pkts))
	}
}

func TestTruncatedCaptureReportsOffset(t *testing.T) {
	spec := DefaultSpec(KindTruncated)
	pkts := Build(spec)
	dir := t.TempDir()
	path, err := WriteCapture(filepath.Join(dir, "cap"), "t.rtp", pkts, 400)
	if err != nil {
		t.Fatal(err)
	}
	_, off, err := ReadCapture(path)
	if !IsTruncated(err) || off < 0 {
		t.Fatalf("expected truncation with offset, got off=%d err=%v", off, err)
	}
}
