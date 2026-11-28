package testmedia

import "testing"

func TestBuildMP4RoundtripShape(t *testing.T) {
	media, err := BuildMP4(DefaultSpec(7))
	if err != nil {
		t.Fatal(err)
	}
	if len(media.Container) == 0 {
		t.Fatal("empty container")
	}
	if len(media.Tracks) != 2 {
		t.Fatalf("tracks = %d; want 2", len(media.Tracks))
	}
	// Determinism: rebuilding from the same spec must yield identical bytes.
	again, err := BuildMP4(DefaultSpec(7))
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Container) != string(media.Container) {
		t.Fatal("same spec produced different bytes across builds")
	}
	// Top-level box order must be ftyp, moov, mdat.
	want := []string{"ftyp", "moov", "mdat"}
	for i, typ := range want {
		off := indexOfBox(media.Container, typ)
		if off < 0 {
			t.Fatalf("missing %s box", typ)
		}
		if i > 0 && off <= indexOfBox(media.Container, want[i-1]) {
			t.Fatalf("box %s out of order", typ)
		}
	}
}

func TestSampleBytesDeterministicAndDistinct(t *testing.T) {
	a := SampleBytes(11, 3, 32)
	b := SampleBytes(11, 3, 32)
	if string(a) != string(b) {
		t.Fatal("same (seed,index) must give same bytes")
	}
	c := SampleBytes(11, 4, 32)
	if string(a) == string(c) {
		t.Fatal("distinct sample indices collided")
	}
}

func TestTruncateAt(t *testing.T) {
	media, _ := BuildMP4(DefaultSpec(7))
	full := media.Container
	cut := TruncateAt(full, len(full)-10)
	if len(cut) != len(full)-10 {
		t.Fatalf("truncated length = %d", len(cut))
	}
	// Truncating inside the mdat header leaves a declared size past EOF.
	mdat := indexOfBox(full, "mdat")
	bad := TruncateAt(full, mdat+4)
	if len(bad) <= mdat {
		t.Fatal("truncation should keep mdat header start")
	}
}
