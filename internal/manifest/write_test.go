package manifest

import (
	"bytes"
	"strings"
	"testing"
)

func sample() *Manifest {
	return &Manifest{
		Seed: 42, Timebase: "24/1", Params: "gop=48",
		Segments: []SegmentEntry{
			{Index: 0, Rep: "v1", StartTC: "00:00:00:00", Frames: 48, SHA256: "aa", Source: "stss"},
			{Index: 1, Rep: "v1", StartTC: "00:00:02:00", Frames: 48, SHA256: "bb", Source: "stss"},
		},
	}
}

func TestDeterministicWrite(t *testing.T) {
	var a, b bytes.Buffer
	if err := Write(&a, sample()); err != nil {
		t.Fatal(err)
	}
	if err := Write(&b, sample()); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatal("same input must render identical bytes")
	}
	if !strings.HasSuffix(a.String(), "\n") {
		t.Fatal("manifest should end with a newline")
	}
}

func TestEqual(t *testing.T) {
	eq, err := Equal(sample(), sample())
	if err != nil || !eq {
		t.Fatalf("identical manifests must compare equal: %v %v", eq, err)
	}
	other := sample()
	other.Segments[1].SHA256 = "cc"
	if eq, _ := Equal(sample(), other); eq {
		t.Fatal("modified digest must break equality")
	}
	other2 := sample()
	other2.Segments[1].Frames++
	if eq, _ := Equal(sample(), other2); eq {
		t.Fatal("modified frame count must break equality")
	}
}
