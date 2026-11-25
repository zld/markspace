package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestSampleHashOrderSensitive(t *testing.T) {
	a := [][]byte{{1, 2, 3}, {4, 5}}
	b := [][]byte{{4, 5}, {1, 2, 3}}
	if HashSamples(a) == HashSamples(b) {
		t.Fatal("hash should depend on sample order")
	}
	if HashSamples(a) != HashSamples([][]byte{{1, 2, 3}, {4, 5}}) {
		t.Fatal("hash should be deterministic")
	}
}

func TestSampleHashBoundaries(t *testing.T) {
	// The length prefix keeps {1,2},{3} distinct from {1},{2,3}.
	if HashSamples([][]byte{{1, 2}, {3}}) == HashSamples([][]byte{{1}, {2, 3}}) {
		t.Fatal("sample boundaries lost in hash")
	}
}

func TestHashReaderAndBytes(t *testing.T) {
	data := []byte("hello world")
	h, n, err := HashReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(data)) {
		t.Fatalf("n = %d", n)
	}
	if h != HashBytes(data) {
		t.Fatalf("stream hash %s != bytes hash %s", h, HashBytes(data))
	}
}

func TestPixelMean(t *testing.T) {
	data := []byte{0, 255, 255, 0}
	mean, err := PixelMean(bytes.NewReader(data), 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if mean < 0.499 || mean > 0.501 {
		t.Fatalf("mean = %f, want 0.5", mean)
	}
	if MeanBytes(data) != mean {
		t.Fatal("in-memory and reader means differ")
	}
}

func TestPixelMeanOffset(t *testing.T) {
	data := make([]byte, 1024)
	for i := 512; i < 1024; i++ {
		data[i] = 200
	}
	mean, err := PixelMean(bytes.NewReader(data), 512, 512)
	if err != nil {
		t.Fatal(err)
	}
	want := 200.0 / 255.0
	if mean < want-1e-9 || mean > want+1e-9 {
		t.Fatalf("mean = %f, want %f", mean, want)
	}
}

func TestStableFormatting(t *testing.T) {
	if got := FormatFloat(0.123456789); got != "0.123457" {
		t.Fatalf("FormatFloat = %s", got)
	}
	if got := FormatMillis(33.3333); got != "33.333" {
		t.Fatalf("FormatMillis = %s", got)
	}
	if strings.HasPrefix(FormatFloat(0.5), "+") {
		t.Fatal("unexpected sign")
	}
}

func TestHasherCounters(t *testing.T) {
	h := NewSampleHasher()
	h.AddSample([]byte{1})
	h.AddSample([]byte{2, 3})
	if h.Count() != 2 || h.Bytes() != 3 {
		t.Fatalf("counters = %d/%d", h.Count(), h.Bytes())
	}
	if len(h.Sum()) != 64 {
		t.Fatalf("digest length = %d", len(h.Sum()))
	}
}
