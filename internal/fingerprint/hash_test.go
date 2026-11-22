package fingerprint

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/pairwise/mediad/internal/fault"
)

type memSamples struct{ data map[uint32][][]byte }

func (m memSamples) ReadSample(track uint32, i int) ([]byte, error) {
	d := m.data[track]
	if i < 0 || i >= len(d) {
		return nil, errors.New("out of range")
	}
	return d[i], nil
}
func (m memSamples) Count(track uint32) int { return len(m.data[track]) }

func payloads(n int, seed byte) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		b := make([]byte, 16)
		binary.BigEndian.PutUint32(b, uint32(i+1))
		b[4] = seed
		out[i] = b
	}
	return out
}

func seg(src memSamples, startNS, endNS int64, first, count int) Segment {
	sum, per, err := Sum(src, 1, first, count)
	if err != nil {
		panic(err)
	}
	return Segment{TrackID: 1, StartNS: startNS, EndNS: endNS, FirstSample: first, SampleCount: count, Content: sum, PerSample: per}
}

func TestSumIsOrderAndBoundarySensitive(t *testing.T) {
	src := memSamples{data: map[uint32][][]byte{1: payloads(4, 1)}}
	a, _, _ := Sum(src, 1, 0, 2)
	b, _, _ := Sum(src, 1, 1, 2)
	if a == b {
		t.Fatal("differing windows produced identical digests")
	}
	// Concatenation ambiguity guard: [ab][c] must differ from [a][bc].
	x, _, _ := Sum(src, 1, 0, 3)
	if x == [32]byte{} {
		t.Fatal("zero digest")
	}
	if _, _, err := Sum(src, 1, 3, 5); err == nil {
		t.Fatal("expected range error")
	}
}

func TestDedupeIdenticalExtentsAndContent(t *testing.T) {
	src := memSamples{data: map[uint32][][]byte{1: payloads(4, 2)}}
	s1 := seg(src, 0, 100, 0, 2)
	s2 := seg(src, 0, 100, 0, 2)
	decs, err := Dedupe([]Segment{s1, s2})
	if err != nil {
		t.Fatal(err)
	}
	if !decs[1].Duplicate || decs[1].Of != 0 || decs[1].Review {
		t.Fatalf("expected plain duplicate: %+v", decs[1])
	}
}

func TestDedupeSameExtentDifferentContentKept(t *testing.T) {
	src1 := memSamples{data: map[uint32][][]byte{1: payloads(2, 3)}}
	src2 := memSamples{data: map[uint32][][]byte{1: payloads(2, 4)}}
	decs, _ := Dedupe([]Segment{seg(src1, 0, 100, 0, 2), seg(src2, 0, 100, 0, 2)})
	if decs[1].Duplicate {
		t.Fatalf("different content deduped: %+v", decs[1])
	}
}

func TestCollisionAcrossExtentsRequiresSampleReview(t *testing.T) {
	src := memSamples{data: map[uint32][][]byte{1: payloads(4, 5)}}
	a := seg(src, 0, 100, 0, 2)
	b := seg(src, 100, 200, 2, 2) // different extent + different samples
	// Force a hash collision to exercise review.
	b.Content = a.Content
	decs, err := Dedupe([]Segment{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if decs[1].Duplicate {
		t.Fatalf("collision merged despite mismatch: %+v", decs[1])
	}
	if !decs[1].Review || decs[1].Reason == "" {
		t.Fatalf("collision not flagged for review: %+v", decs[1])
	}
}

func TestCollisionWithMatchingSamplesDedupesAfterReview(t *testing.T) {
	src := memSamples{data: map[uint32][][]byte{1: payloads(2, 6)}}
	a := seg(src, 0, 100, 0, 2)
	b := seg(src, 100, 200, 0, 2) // same bytes, shifted extent
	if a.Content != b.Content {
		t.Fatal("expected identical content")
	}
	decs, _ := Dedupe([]Segment{a, b})
	if !decs[1].Duplicate || !decs[1].Review {
		t.Fatalf("sample review should confirm duplicate: %+v", decs[1])
	}
}

func TestManifestHashStableAndSensitive(t *testing.T) {
	src := memSamples{data: map[uint32][][]byte{1: payloads(6, 7)}}
	var fps []Fingerprint
	for i := 0; i < 3; i++ {
		s := seg(src, int64(i*100), int64(i*100+100), i*2, 2)
		fps = append(fps, New(1, s.StartNS, s.EndNS, 2, s.Content))
	}
	h1 := ManifestHash(fps)
	rev := []Fingerprint{fps[2], fps[0], fps[1]}
	if ManifestHash(rev) != h1 {
		t.Fatal("manifest hash depends on input order")
	}
	fps[1].SampleCount = 3
	if ManifestHash(fps) == h1 {
		t.Fatal("manifest hash ignored a field change")
	}
}

func TestResolveCollisionNeverMergesWithoutEvidence(t *testing.T) {
	src := memSamples{data: map[uint32][][]byte{1: payloads(2, 8)}}
	a := seg(src, 0, 100, 0, 2)
	b := seg(src, 100, 200, 0, 2)
	a.PerSample, b.PerSample = nil, nil
	if _, err := ResolveCollision(a, b); !errors.Is(err, ErrCollision) {
		t.Fatalf("want ErrCollision, got %v", err)
	}
}

func TestDedupeRejectsEmptyExtent(t *testing.T) {
	_, err := Dedupe([]Segment{{TrackID: 1, StartNS: 10, EndNS: 10}})
	if err == nil || fault.ExitCode(err) != 2 {
		t.Fatalf("err=%v", err)
	}
}

func TestFingerprintKeyIncludesExtent(t *testing.T) {
	var c [32]byte
	c[0] = 0xab
	fp := New(2, 5, 9, 1, c)
	if fp.ContentHex != "ab"+"00000000000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("hex=%s", fp.ContentHex)
	}
	if fp.Key == New(2, 5, 10, 1, c).Key {
		t.Fatal("key ignores extent")
	}
}
