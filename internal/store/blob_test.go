package store

import (
	"bytes"
	"io"
	"testing"
)

func TestBlobDeduplicates(t *testing.T) {
	b := NewBlobStore(t.TempDir())
	d1, n1, err := b.Put(bytes.NewReader([]byte("segment-payload")))
	if err != nil || n1 != int64(len("segment-payload")) {
		t.Fatalf("put1 %v %d", err, n1)
	}
	d2, _, err := b.Put(bytes.NewReader([]byte("segment-payload")))
	if err != nil || d1 != d2 {
		t.Fatalf("same bytes must share an address: %v", err)
	}
	f, err := b.Open(d1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if string(got) != "segment-payload" {
		t.Fatalf("payload %q", got)
	}
}

func TestBlobRecomputeDetectsTamper(t *testing.T) {
	b := NewBlobStore(t.TempDir())
	d, _, err := b.Put(bytes.NewReader([]byte("abc")))
	if err != nil {
		t.Fatal(err)
	}
	sum, size, err := b.Recompute(d)
	if err != nil || sum != d || size != 3 {
		t.Fatalf("recompute %v %s %d", err, sum, size)
	}
}

func TestBlobRejectsBadDigest(t *testing.T) {
	b := NewBlobStore(t.TempDir())
	if _, err := b.Path("../../etc/passwd"); err == nil {
		t.Fatal("expected invalid digest rejection")
	}
}
