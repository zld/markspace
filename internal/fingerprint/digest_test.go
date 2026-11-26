package fingerprint

import (
	"testing"

	"example.com/mediavault/internal/clock"
	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/model"
)

func format(rate int64, channels, bits int) model.WAVFormat {
	return model.WAVFormat{
		SampleRate:    clock.NewRate(rate, 1),
		Channels:      channels,
		BitsPerSample: bits,
	}
}

func refsFrom(pcm ...[]byte) []model.ChunkRef {
	out := make([]model.ChunkRef, len(pcm))
	for i, p := range pcm {
		out[i] = model.ChunkRef{Index: i, Frames: int64(len(p)), Length: int64(len(p)), Digest: BlobDigest(p)}
	}
	return out
}

func TestBlobDigestIs64HexAndStable(t *testing.T) {
	a := BlobDigest([]byte("samples"))
	if !ValidDigest(a) {
		t.Fatalf("digest %q is not 64 hex", a)
	}
	if BlobDigest([]byte("samples")) != a {
		t.Fatal("digest is not deterministic")
	}
	if BlobDigest([]byte("Samples")) == a {
		t.Fatal("different bytes must not share a digest")
	}
}

func TestChunkDigestAddressesBytesOnly(t *testing.T) {
	pcm := []byte("block payload")
	// Object identity is the bytes; an equal payload in another position must
	// be the same content address so the store can deduplicate it.
	if ChunkDigest(0, 5, pcm) != ChunkDigest(7, 99, pcm) {
		t.Fatal("chunk digest should address the bytes alone")
	}
	if ChunkDigest(0, 5, pcm) == ChunkDigest(0, 5, []byte("block payloae")) {
		t.Fatal("digest collided on different bytes")
	}
}

func TestAggregateSeparatesPositionsAndPolicy(t *testing.T) {
	f := format(8000, 1, 16)
	a := refsFrom([]byte("aaaa"), []byte("bbbb"))
	b := refsFrom([]byte("bbbb"), []byte("aaaa"))
	policy := Policy{Version: PolicyVersion, ChunkFrames: 512}
	da, err := Aggregate(policy, f, a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Aggregate(policy, f, b)
	if err != nil {
		t.Fatal(err)
	}
	if da == db {
		t.Fatal("reordered identical blocks must not share an aggregate digest")
	}
	other := Policy{Version: PolicyVersion, ChunkFrames: 256}
	dc, err := Aggregate(other, f, a)
	if err != nil {
		t.Fatal(err)
	}
	if dc == da {
		t.Fatal("a policy change must change the derived asset digest")
	}
	if _, err := Aggregate(Policy{}, f, a); fault.CodeOf(err) != fault.CodePolicyVersion {
		t.Fatalf("invalid policy code = %q", fault.CodeOf(err))
	}
}

func TestAggregateIgnoresContainerDigest(t *testing.T) {
	// Two assets with the same samples but different wrappers must derive the
	// same content digest: PCM products are reusable when only packaging moves.
	f := format(8000, 2, 24)
	chunks := refsFrom([]byte("left"), []byte("right"))
	policy := Policy{Version: PolicyVersion, ChunkFrames: 512}
	a, err := Aggregate(policy, f, chunks)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Aggregate(policy, f, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("aggregate is not stable")
	}
}

func TestChangedChunksReportsSingleEdit(t *testing.T) {
	prev := refsFrom([]byte("0"), []byte("1"), []byte("2"), []byte("3"))
	next := refsFrom([]byte("0"), []byte("X"), []byte("2"), []byte("3"))
	changed := ChangedChunks(prev, next)
	if len(changed) != 1 || changed[0] != 1 {
		t.Fatalf("changed = %v, want [1]", changed)
	}
	if SamePCM(prev, next) {
		t.Fatal("SamePCM should detect the edit")
	}
	if !SamePCM(prev, prev) {
		t.Fatal("SamePCM should accept identical chunk sets")
	}
}

func TestChangedChunksDetectsAddsAndRemoves(t *testing.T) {
	prev := refsFrom([]byte("0"), []byte("1"), []byte("2"))
	next := refsFrom([]byte("0"), []byte("1"))
	changed := ChangedChunks(prev, next)
	if len(changed) != 1 || changed[0] != 2 {
		t.Fatalf("removed block not reported: %v", changed)
	}
	longer := refsFrom([]byte("0"), []byte("1"), []byte("9"), []byte("3"))
	changed = ChangedChunks(prev, longer)
	if len(changed) != 2 {
		t.Fatalf("added and changed blocks not reported: %v", changed)
	}
}

func TestPolicyKeepsBlocksFrameAligned(t *testing.T) {
	for _, c := range []struct{ channels, bits int }{{1, 16}, {2, 24}, {8, 32}, {6, 16}} {
		p := DefaultPolicy(c.channels, c.bits)
		frame := int64(c.channels * c.bits / 8)
		if p.ChunkFrames*frame > 1<<20 {
			t.Fatalf("%d ch %d bit block exceeds 1 MiB", c.channels, c.bits)
		}
		if p.ChunkFrames <= 0 {
			t.Fatalf("%d ch %d bit has no frames", c.channels, c.bits)
		}
	}
}

func TestValidDigestRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "abc", string(make([]byte, 64)), "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if ValidDigest(s) {
			t.Fatalf("%q accepted as a digest", s)
		}
	}
}
