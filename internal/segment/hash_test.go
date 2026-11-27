package segment

import "testing"

func sampleSegment() *Segment {
	return &Segment{
		ID: 1, Representation: 2,
		SourceStart: 0, SourceEnd: 1880,
		StartNanos: 0, EndNanos: 2_000_000_000,
		SampleCount: 60,
		KeyframeIndex: []IndexEntry{
			{SampleIndex: 0, KeyframePID: 0x100, StartNanos: 0, EndNanos: 33_333_333, ByteOffset: 0},
			{SampleIndex: 30, KeyframePID: 0x100, StartNanos: 1_000_000_000, EndNanos: 1_033_333_333, ByteOffset: 940},
		},
	}
}

func TestHashDeterministic(t *testing.T) {
	a := sampleSegment()
	b := sampleSegment()
	if Hash(a) != Hash(b) {
		t.Fatal("identical segments must hash identically")
	}
	if len(Hash(a)) != 64 {
		t.Fatalf("hex digest length=%d want 64", len(Hash(a)))
	}
}

func TestCanonicalSensitiveToEveryField(t *testing.T) {
	base := sampleSegment()
	baseHash := Hash(base)

	mutants := []func(*Segment){
		func(s *Segment) { s.ID = 9 },
		func(s *Segment) { s.Representation = 3 },
		func(s *Segment) { s.SourceEnd = 9999 },
		func(s *Segment) { s.StartNanos = 5 },
		func(s *Segment) { s.SampleCount = 61 },
		func(s *Segment) { s.KeyframeIndex[0].SampleIndex = 1 },
		func(s *Segment) { s.KeyframeIndex = s.KeyframeIndex[:1] },
	}
	for i, mutate := range mutants {
		s := sampleSegment()
		mutate(s)
		if Hash(s) == baseHash {
			t.Errorf("mutant %d did not change the hash", i)
		}
	}
}

func TestCanonicalLengthPrefixed(t *testing.T) {
	s := sampleSegment()
	b := Canonical(s)
	// Header: 7 int64 fields (56) + 4-byte entry count. Each entry: 2-byte
	// PID + 4-byte sample index + 3 int64 times (24) = 30 bytes.
	want := 56 + 4 + 2*30
	if len(b) != want {
		t.Errorf("canonical length=%d want %d", len(b), want)
	}
}

func TestEmptyIndexStillEncodesCount(t *testing.T) {
	s := sampleSegment()
	s.KeyframeIndex = nil
	b := Canonical(s)
	if len(b) != 56+4 {
		t.Errorf("length=%d want 60", len(b))
	}
	if Hash(s) == Hash(sampleSegment()) {
		t.Error("nil vs populated index must differ")
	}
}
