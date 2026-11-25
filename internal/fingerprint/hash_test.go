package fingerprint

import (
	"encoding/binary"
	"testing"

	"mediad/internal/mp4"
)

func putFreeSize(b []byte, n uint32) {
	binary.BigEndian.PutUint32(b[:4], n)
	copy(b[4:8], "free")
}

func TestContentHashStable(t *testing.T) {
	payload := []byte("media-bytes-1234")
	h1 := ContentHash(payload)
	h2 := ContentHash(append([]byte{}, payload...))
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash must be stable and sha256 hex: %q %q", h1, h2)
	}
	if ContentHash([]byte("different")) == h1 {
		t.Fatal("distinct content must hash differently")
	}
}

func TestHashFragmentOffsetIndependent(t *testing.T) {
	// Two files with identical sample payloads but located at different
	// offsets must produce the same fingerprint.
	data1 := mp4.BuildTestFragment(1, 0, 8)
	r1 := mp4.NewSeeker(data1)
	b1, _ := mp4.ScanTopLevel(r1, "a.m4s", mp4.Limits{})
	f1, _ := mp4.DecodeFragment(r1, "a.m4s", b1[0], b1[1], mp4.Limits{})

	prefix := make([]byte, 24)
	putFreeSize(prefix, 24)
	data2 := append(prefix, data1...)
	r2 := mp4.NewSeeker(data2)
	b2, _ := mp4.ScanTopLevel(r2, "b.m4s", mp4.Limits{})
	var moof, mdat *mp4.Box
	for _, bx := range b2 {
		switch bx.Type {
		case "moof":
			moof = bx
		case "mdat":
			mdat = bx
		}
	}
	f2, _ := mp4.DecodeFragment(r2, "b.m4s", moof, mdat, mp4.Limits{})

	hA := HashFragment(data1, f1.Tracks[0])
	hB := HashFragment(data2, f2.Tracks[0])
	if hA == "" || hA != hB {
		t.Fatalf("identical content at different offsets must share a hash: %q vs %q", hA, hB)
	}
}

func TestDedupMergesSameContent(t *testing.T) {
	d := NewDedup()
	fp1 := d.Add(Candidate{Key: "hashA", ContentOK: true, Offset: 100})
	fp2 := d.Add(Candidate{Key: "hashA", ContentOK: true, Offset: 999})
	if fp1.Hash != fp2.Hash || fp2.FirstSeen != 100 {
		t.Fatalf("same content must point to one fingerprint: %+v %+v", fp1, fp2)
	}
	if len(d.Fingerprints()) != 1 {
		t.Fatalf("dedup must hold one canonical entry: %+v", d.Fingerprints())
	}
}

func TestDedupKeepsCollisionCandidatesIndependent(t *testing.T) {
	d := NewDedup()
	_ = d.Add(Candidate{Key: "hashA", ContentOK: true, Offset: 10})
	collision := d.Add(Candidate{Key: "hashA", ContentOK: false, Offset: 20})
	if collision.Hash == "hashA" {
		t.Fatalf("collision candidate must be kept independent: %+v", collision)
	}
	fps := d.Fingerprints()
	if len(fps) != 2 {
		t.Fatalf("want 2 independent entries, got %d: %+v", len(fps), fps)
	}
}

func TestDedupDeterministicOrder(t *testing.T) {
	build := func() []Fingerprint {
		d := NewDedup()
		for _, k := range []string{"hashC", "hashA", "hashB"} {
			d.Add(Candidate{Key: k, ContentOK: true, Offset: 0})
		}
		return d.Fingerprints()
	}
	a, b := build(), build()
	if len(a) != 3 || a[0].Hash != "hashA" || a[2].Hash != "hashC" {
		t.Fatalf("not sorted deterministically: %+v", a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("repeated builds must be identical: %+v vs %+v", a, b)
		}
	}
}
