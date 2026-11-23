package fixture

import (
	"bytes"
	"testing"

	"mediad/internal/ebml"
)

func baseResult(t *testing.T) *Result {
	t.Helper()
	r, err := Build(Spec{Seed: 42, DurationSec: 10, VideoTracks: 1, AudioTracks: 1, Representations: 3, UnknownClusters: 1})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildDeterministic(t *testing.T) {
	a := baseResult(t)
	b := baseResult(t)
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatal("generation is not deterministic for the same seed")
	}
	c, err := Build(Spec{Seed: 43, DurationSec: 10, VideoTracks: 1, AudioTracks: 1, Representations: 3, UnknownClusters: 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Data, c.Data) {
		t.Fatal("different seeds produced identical data")
	}
}

func TestBuildParsesCleanly(t *testing.T) {
	r := baseResult(t)
	var blocks int
	doc, err := ebml.Parse(bytes.NewReader(r.Data), ebml.DefaultParseOptions(), func(ebml.Block) error {
		blocks++
		return nil
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if blocks == 0 || doc.ElementCount < 500 {
		t.Fatalf("blocks=%d elements=%d, want >=500 elements", blocks, doc.ElementCount)
	}
	if len(doc.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(doc.Tracks))
	}
	if !doc.Segment.UnknownSize {
		// Default spec uses a known-size segment; this base sets UnknownClusters
		// only. Accept either but require parse success.
		_ = doc
	}
}

func TestUnknownSizeSegmentParses(t *testing.T) {
	r, err := Build(Spec{Seed: 7, DurationSec: 3, UnknownSegment: true, UnknownClusters: 2})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ebml.Parse(bytes.NewReader(r.Data), ebml.DefaultParseOptions(), nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !doc.Segment.UnknownSize {
		t.Fatal("segment should be unknown-size")
	}
}

func TestCorruptCasesAreDeterministic(t *testing.T) {
	base := baseResult(t)
	a, err := Corrupt(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Corrupt(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 5 || len(b) != 5 {
		t.Fatalf("case count = %d/%d, want 5", len(a), len(b))
	}
	for i := range a {
		if a[i].Kind != b[i].Kind {
			t.Fatalf("case %d kind drift", i)
		}
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("case %d data drift", i)
		}
	}
}

func TestTruncatedElementFailsParse(t *testing.T) {
	base := baseResult(t)
	cases, err := Corrupt(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := cases[0]
	if c.Kind != CorruptTruncatedElement {
		t.Fatalf("unexpected first case %s", c.Kind)
	}
	_, perr := ebml.Parse(bytes.NewReader(c.Data), ebml.DefaultParseOptions(), nil)
	var pe *ebml.ParseError
	if perr == nil {
		t.Fatal("truncated fixture parsed without error")
	}
	if !asParseError(perr, &pe) || pe.Kind != ebml.ErrKindTruncated {
		t.Fatalf("err = %v", perr)
	}
}

func TestInvalidVINTFailsParse(t *testing.T) {
	base := baseResult(t)
	cases, _ := Corrupt(base, 1)
	c := cases[1]
	if c.Kind != CorruptInvalidVINT {
		t.Fatalf("unexpected case %s", c.Kind)
	}
	_, perr := ebml.Parse(bytes.NewReader(c.Data), ebml.DefaultParseOptions(), nil)
	var pe *ebml.ParseError
	if perr == nil || !asParseError(perr, &pe) || pe.Kind != ebml.ErrKindInvalidVINT {
		t.Fatalf("err = %v", perr)
	}
}

func TestTimestampReverseFixtureParses(t *testing.T) {
	// Parse succeeds; the timeline stage is what rejects it.
	base := baseResult(t)
	cases, _ := Corrupt(base, 1)
	_, err := ebml.Parse(bytes.NewReader(cases[2].Data), ebml.DefaultParseOptions(), nil)
	if err != nil {
		t.Fatalf("reverse-timestamp fixture should parse: %v", err)
	}
}

func TestMisalignFixtureMutatesKeyframes(t *testing.T) {
	base := baseResult(t)
	cases, _ := Corrupt(base, 1)
	c := cases[3]
	if len(c.MutatedKeyframes) < 2 {
		t.Fatalf("misalign case has no mutated keyframes")
	}
	delta := c.MutatedKeyframes[1] - base.VideoKeyframes[1]
	if delta <= 33_333_333 {
		t.Fatalf("misalignment %d is within one frame", delta)
	}
}

func asParseError(err error, out **ebml.ParseError) bool {
	for err != nil {
		if pe, ok := err.(*ebml.ParseError); ok {
			*out = pe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
