package segment

import (
	"strings"
	"testing"

	"github.com/example/tsrepair/internal/index"
)

func TestSegmentHashesAndRecompute(t *testing.T) {
	h := NewHasher(3)
	var all []byte
	for i := int64(0); i < 7; i++ {
		raw := []byte{byte(i), 2, 3, 4} // stand-in for a packet body
		all = append(all, raw...)
		h.AddPacket(i, i*4, uint64(i*90000), raw)
	}
	if !h.Flush(7, 28) {
		t.Fatal("partial final segment must flush")
	}
	sums := h.Summaries()
	if len(sums) != 3 {
		t.Fatalf("segments=%d want 3 (3,3,1)", len(sums))
	}
	if sums[0].PacketCount != 3 || sums[2].PacketCount != 1 {
		t.Fatalf("segment sizes wrong: %d %d", sums[0].PacketCount, sums[2].PacketCount)
	}
	if sums[0].StartPacket != 0 || sums[0].EndPacket != 3 ||
		sums[0].StartOffset != 0 || sums[0].EndOffset != 12 {
		t.Fatalf("first segment ranges wrong: %+v", sums[0])
	}
	if !sums[0].Recompute(all[0:12]) {
		t.Fatal("recomputed hash must match the stored digest")
	}
	if sums[0].Recompute(append([]byte{9}, all[1:12]...)) {
		t.Fatal("tampered data must not match")
	}
}

func TestDeterministicHashesAcrossRuns(t *testing.T) {
	run := func() []Summary {
		h := NewHasher(2)
		for i := int64(0); i < 4; i++ {
			h.AddPacket(i, i*188, uint64(i), []byte{byte(i), 0x47})
		}
		h.Flush(4, 4*188)
		return h.Summaries()
	}
	a, b := run(), run()
	for i := range a {
		if a[i].SHA256 != b[i].SHA256 {
			t.Fatalf("nondeterministic digest at %d", i)
		}
	}
}

func TestKeyframeIndexRanges(t *testing.T) {
	summaries := []Summary{
		{Seq: 0, StartPacket: 0, EndPacket: 4},
		{Seq: 1, StartPacket: 4, EndPacket: 8},
	}
	ix := &index.Index{Frames: []index.Keyframe{
		{ID: 0, PacketOffset: 1},
		{ID: 1, PacketOffset: 5},
		{ID: 2, PacketOffset: 6},
	}}
	AttachIndexRanges(summaries, ix)
	if summaries[0].IndexRange != [2]int{0, 1} {
		t.Fatalf("segment 0 range=%v", summaries[0].IndexRange)
	}
	if summaries[1].IndexRange != [2]int{1, 3} {
		t.Fatalf("segment 1 range=%v", summaries[1].IndexRange)
	}
}

func TestReportContainsAllFields(t *testing.T) {
	h := NewHasher(1)
	h.AddPacket(0, 0, 0, []byte("x"))
	h.Flush(1, 1)
	line := h.Summaries()[0].Report()
	for _, want := range []string{"segment=0", "packets=[0,1)", "bytes=[0,1)", "pts=[0,0]", "sha256="} {
		if !strings.Contains(line, want) {
			t.Fatalf("report %q missing %q", line, want)
		}
	}
}
