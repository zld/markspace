package segment

import (
	"testing"

	"example.com/mediaforge/internal/merrors"
)

func mkSeg(idx int, payloads ...string) Segment {
	s := Segment{Index: idx, StartTick: uint64(idx * 1000), EndTick: uint64(idx*1000 + 1000)}
	for i, p := range payloads {
		s.Samples = append(s.Samples, Sample{
			TrackSSRC:  1,
			CommonTick: uint64(idx*1000 + i*100),
			Duration:   100,
			Keyframe:   i == 0,
			Payload:    []byte(p),
		})
	}
	return s
}

func TestHashDeterministic(t *testing.T) {
	seg := mkSeg(0, "alpha", "beta", "gamma")
	a, err := Compute(seg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compute(seg)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 {
		t.Fatalf("hashes differ: %x vs %x", a.SHA256, b.SHA256)
	}
	if a.Hex() != b.Hex() || len(a.Hex()) != 64 {
		t.Fatalf("hex = %q", a.Hex())
	}
	if a.Samples != 3 || a.Bytes != 14 {
		t.Fatalf("hash = %+v", a)
	}
}

func TestHashSensitiveToEveryField(t *testing.T) {
	base := mkSeg(0, "alpha", "beta")
	want, err := Compute(base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Segment){
		"payload":   func(s *Segment) { s.Samples[1].Payload = []byte("betA") },
		"tick":      func(s *Segment) { s.Samples[1].CommonTick++ },
		"duration":  func(s *Segment) { s.Samples[1].Duration++ },
		"keyframe":  func(s *Segment) { s.Samples[1].Keyframe = true },
		"recovered": func(s *Segment) { s.Samples[1].Recovered = true },
		"ssrc":      func(s *Segment) { s.Samples[1].TrackSSRC = 2 },
		"start":     func(s *Segment) { s.StartTick++ },
		"end":       func(s *Segment) { s.EndTick++ },
		"index":     func(s *Segment) { s.Index = 1 },
		"order":     func(s *Segment) { s.Samples[0], s.Samples[1] = s.Samples[1], s.Samples[0] },
		"drop":      func(s *Segment) { s.Samples = s.Samples[:1] },
	}
	for name, mut := range mutations {
		s := mkSeg(0, "alpha", "beta")
		mut(&s)
		got, err := Compute(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.SHA256 == want.SHA256 {
			t.Fatalf("%s: hash unchanged", name)
		}
	}
}

func TestHashLengthPrefixPreventsAmbiguity(t *testing.T) {
	// Without length prefixes, ["ab","c"] and ["a","bc"] would collide.
	a := Segment{Index: 0, Samples: []Sample{
		{TrackSSRC: 1, Payload: []byte("ab")},
		{TrackSSRC: 1, Payload: []byte("c")},
	}}
	b := Segment{Index: 0, Samples: []Sample{
		{TrackSSRC: 1, Payload: []byte("a")},
		{TrackSSRC: 1, Payload: []byte("bc")},
	}}
	ha, err := Compute(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := Compute(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha.SHA256 == hb.SHA256 {
		t.Fatal("ambiguous framing")
	}
}

func TestComputeAllAndEqual(t *testing.T) {
	segs := []Segment{mkSeg(0, "a"), mkSeg(1, "b"), mkSeg(2, "c")}
	a, err := ComputeAll(segs)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComputeAll(segs)
	if err != nil {
		t.Fatal(err)
	}
	if idx, ok := Equal(a, b); !ok || idx != -1 {
		t.Fatalf("Equal = %d %v", idx, ok)
	}
	b[1].SHA256[0] ^= 0xff
	if idx, ok := Equal(a, b); ok || idx != 1 {
		t.Fatalf("Equal = %d %v", idx, ok)
	}
	if idx, ok := Equal(a, a[:2]); ok || idx != 2 {
		t.Fatalf("length mismatch = %d %v", idx, ok)
	}
}

func TestDamageAffectsOnlyItsSegment(t *testing.T) {
	orig := []Segment{mkSeg(0, "aaa"), mkSeg(1, "bbb"), mkSeg(2, "ccc")}
	before, err := ComputeAll(orig)
	if err != nil {
		t.Fatal(err)
	}
	// A recovered packet changes only segment 1's payload flag and bytes.
	// mkSeg builds one sample per payload string, so segment 1 has a single
	// sample; regenerate it with two before mutating.
	repaired := []Segment{mkSeg(0, "aaa"), mkSeg(1, "bbb", "bbb2"), mkSeg(2, "ccc")}
	repaired[1].Samples[0].Payload = []byte("bbb")
	repaired[1].Samples[0].Recovered = true
	repaired[1].Samples[1].Payload = []byte("bbX")
	after, err := ComputeAll(repaired)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].SHA256 != after[0].SHA256 {
		t.Fatal("segment 0 changed")
	}
	if before[1].SHA256 == after[1].SHA256 {
		t.Fatal("segment 1 unchanged despite damage")
	}
	if before[2].SHA256 != after[2].SHA256 {
		t.Fatal("segment 2 changed")
	}
}

func TestComputeValidation(t *testing.T) {
	if _, err := Compute(Segment{Index: -1}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("negative index err = %v", err)
	}
	if _, err := Compute(Segment{Index: 0, StartTick: 10, EndTick: 5}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("inverted bounds err = %v", err)
	}
	big := Segment{Index: 0, Samples: make([]Sample, MaxSamplesPerSegment+1)}
	if _, err := Compute(big); !merrors.IsCategory(err, merrors.Bounds) {
		t.Fatalf("sample count err = %v", err)
	}
	huge := Segment{Index: 0, Samples: []Sample{{Payload: make([]byte, MaxPayloadBytes+1)}}}
	if _, err := Compute(huge); !merrors.IsCategory(err, merrors.Bounds) {
		t.Fatalf("payload err = %v", err)
	}
	// Duplicate indices are rejected by ComputeAll.
	if _, err := ComputeAll([]Segment{mkSeg(0), mkSeg(0)}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestVerify(t *testing.T) {
	segs := []Segment{mkSeg(0, "a"), mkSeg(1, "b")}
	hashes, err := ComputeAll(segs)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(segs, hashes); err != nil {
		t.Fatalf("verify: %v", err)
	}
	hashes[0].SHA256[0] ^= 1
	if err := Verify(segs, hashes); !merrors.IsCategory(err, merrors.Checksum) {
		t.Fatalf("checksum err = %v", err)
	}
	if err := Verify(segs, hashes[:1]); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("length err = %v", err)
	}
}

func TestEmptySegment(t *testing.T) {
	h, err := Compute(Segment{Index: 4})
	if err != nil {
		t.Fatal(err)
	}
	if h.Samples != 0 || h.Bytes != 0 {
		t.Fatalf("empty hash = %+v", h)
	}
	h2, _ := Compute(Segment{Index: 4})
	if h.SHA256 != h2.SHA256 {
		t.Fatal("empty hash nondeterministic")
	}
}
