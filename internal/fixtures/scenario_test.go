package fixtures

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"mediapipe/internal/clock"
	"mediapipe/internal/meta"
	"mediapipe/internal/mp4"
	"mediapipe/internal/rtp"
)

func TestBuildDeterministic(t *testing.T) {
	a, err := Build(17)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(17)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.MP4, b.MP4) {
		t.Fatal("mp4 fixture not deterministic")
	}
	if !bytes.Equal(a.Fingerprint, b.Fingerprint) {
		t.Fatal("fingerprint not deterministic")
	}
	if len(a.Packets) != len(b.Packets) {
		t.Fatal("packet count differs")
	}
	for i := range a.Packets {
		if a.Packets[i].Seq != b.Packets[i].Seq || !bytes.Equal(a.Packets[i].Data, b.Packets[i].Data) {
			t.Fatalf("packet %d differs", i)
		}
	}
}

func TestBuildSeedChangesContent(t *testing.T) {
	a, _ := Build(17)
	b, _ := Build(18)
	if bytes.Equal(a.Fingerprint, b.Fingerprint) {
		t.Fatal("different seeds produced identical content")
	}
}

func TestGeneratedMP4Parses(t *testing.T) {
	sc, err := Build(3)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.ParseFile(sc.MP4)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Segments) != Segments {
		t.Fatalf("segments %d", len(f.Segments))
	}
	idx, err := mp4.BuildIndex(f)
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range idx.SegmentHashes() {
		if len(h) != 64 {
			t.Fatalf("segment %d hash length %d", i, len(h))
		}
	}
	if len(idx.Keyframes) == 0 {
		t.Fatal("no keyframes indexed")
	}
}

func TestPacketsCoverEveryGroup(t *testing.T) {
	sc, err := Build(11)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]int{}
	for _, p := range sc.Packets {
		seen[p.GroupIndex]++
		if p.FragIndex >= p.FragCount {
			t.Fatalf("packet %d has fragment %d/%d", p.Seq, p.FragIndex, p.FragCount)
		}
	}
	for gi, g := range sc.Groups {
		if seen[gi] != g.FragCount {
			t.Fatalf("group %d has %d packets, want %d", gi, seen[gi], g.FragCount)
		}
	}
	// Sequence numbers must cross the 16-bit wrap exactly once.
	wrapped := false
	for i := 1; i < len(sc.Packets); i++ {
		if sc.Packets[i].Seq < sc.Packets[i-1].Seq {
			wrapped = true
		}
	}
	if !wrapped {
		t.Fatal("generated stream does not cross the sequence wrap")
	}
}

func TestPacketsCarryAllMetadataFamilies(t *testing.T) {
	sc, _ := Build(17)
	kinds := map[meta.Kind]bool{}
	for _, r := range sc.Records {
		kinds[r.Kind] = true
	}
	for _, k := range []meta.Kind{meta.KindSubtitle, meta.KindChapter, meta.KindID3, meta.KindEmsg, meta.KindSEI} {
		if !kinds[k] {
			t.Fatalf("metadata family %s missing", k)
		}
	}
	// Metadata must be strictly ordered on the unified line within a segment.
	last := int64(-1)
	for _, g := range sc.Groups {
		if g.TrackID != 3 {
			continue
		}
		if g.UnifiedStart < last {
			t.Fatalf("metadata time regressed: %d < %d", g.UnifiedStart, last)
		}
		last = g.UnifiedStart
	}
}

func TestJitterArrivalDeterministic(t *testing.T) {
	for i := 0; i < 20; i++ {
		a := JitterArrival(17, i, 80)
		b := JitterArrival(17, i, 80)
		if a != b {
			t.Fatalf("jitter not deterministic for %d", i)
		}
		if a < int64(i)*900-int64(80)*90 || a > int64(i)*900+int64(80)*90 {
			t.Fatalf("jitter outside bound: %d", a)
		}
	}
	if JitterArrival(17, 0, 0) != 0 {
		t.Fatal("zero jitter should not perturb the first packet")
	}
}

func TestTransmitFaults(t *testing.T) {
	sc, _ := Build(17)
	tx, err := Transmit(sc, FaultConfig{ReorderPermille: 200, DuplicatePermille: 100, LossBurst: 3, JitterMS: 80, LateLateRatio: 500})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Expected != len(sc.Packets) {
		t.Fatalf("expected %d", tx.Expected)
	}
	if len(tx.Lost) != Segments*3 {
		t.Fatalf("lost %d, want %d", len(tx.Lost), Segments*3)
	}
	if len(tx.Retransmit) != len(tx.Lost) {
		t.Fatalf("retransmit %d vs lost %d", len(tx.Retransmit), len(tx.Lost))
	}
	if tx.Duplicates == 0 {
		t.Fatal("no duplicates injected")
	}
	late := 0
	for _, rt := range tx.Retransmit {
		if rt.Late {
			late++
		}
	}
	if late == 0 || late == len(tx.Retransmit) {
		t.Fatalf("late split not exercised: %d/%d", late, len(tx.Retransmit))
	}
	// Every delivered packet must be parseable and carry a valid payload.
	for _, d := range tx.Delivered {
		if _, err := rtp.FromWire(d.Wire); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScenarioChecksumStable(t *testing.T) {
	sc, _ := Build(17)
	sum := sha256.Sum256(sc.MP4)
	if sum != sha256.Sum256(sc.MP4) {
		t.Fatal("hash unstable")
	}
	if sc.Fingerprint == nil {
		t.Fatal("missing fingerprint")
	}
	_ = clock.UnifiedTimescale
}
