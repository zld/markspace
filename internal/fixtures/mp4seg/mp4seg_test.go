package mp4seg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sampleSegment() Segment {
	payloads := [][]byte{
		bytes.Repeat([]byte{0x11}, 120),
		bytes.Repeat([]byte{0x22}, 240),
		bytes.Repeat([]byte{0x33}, 80),
		bytes.Repeat([]byte{0x44}, 300),
	}
	seg := Segment{TimeScale: 1_000_000_000, Width: 64, Height: 48}
	for i, p := range payloads {
		seg.Samples = append(seg.Samples, Sample{Data: p, Ticks: int64(i) * 40_000_000})
	}
	return seg
}

func TestWriteReadRoundTrip(t *testing.T) {
	seg := sampleSegment()
	var buf bytes.Buffer
	if err := Write(&buf, seg); err != nil {
		t.Fatal(err)
	}
	meta, payloads, err := Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Width != 64 || meta.Height != 48 || meta.TimeScale != 1_000_000_000 {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.Codec != "av01" {
		t.Fatalf("codec = %s", meta.Codec)
	}
	if len(payloads) != len(seg.Samples) {
		t.Fatalf("samples = %d want %d", len(payloads), len(seg.Samples))
	}
	for i, s := range seg.Samples {
		if !bytes.Equal(payloads[i], s.Data) {
			t.Fatalf("sample %d payload differs", i)
		}
	}
	if len(meta.Deltas) != len(seg.Samples) {
		t.Fatalf("deltas = %d", len(meta.Deltas))
	}
	for i := 1; i < len(meta.Deltas); i++ {
		if meta.Deltas[i] != 40_000_000 {
			t.Fatalf("delta %d = %d want 40", i, meta.Deltas[i])
		}
	}
	if len(meta.ChunkOffset) != len(seg.Samples) {
		t.Fatalf("chunk offsets = %d", len(meta.ChunkOffset))
	}
	for i, off := range meta.ChunkOffset {
		if off == 0 || int(off) >= buf.Len() {
			t.Fatalf("offset %d = %d outside file", i, off)
		}
	}
}

func TestWriteDeterministic(t *testing.T) {
	seg := sampleSegment()
	var a, b bytes.Buffer
	if err := Write(&a, seg); err != nil {
		t.Fatal(err)
	}
	if err := Write(&b, seg); err != nil {
		t.Fatal(err)
	}
	ha := sha256.Sum256(a.Bytes())
	hb := sha256.Sum256(b.Bytes())
	if ha != hb {
		t.Fatalf("segment hashes differ: %s vs %s",
			hex.EncodeToString(ha[:]), hex.EncodeToString(hb[:]))
	}
}

func TestWriteRejectsBadTimeline(t *testing.T) {
	seg := sampleSegment()
	seg.Samples[2].Ticks = seg.Samples[1].Ticks
	if err := Write(&bytes.Buffer{}, seg); err == nil {
		t.Fatal("equal timestamps accepted")
	}
	seg = sampleSegment()
	seg.Samples = nil
	if err := Write(&bytes.Buffer{}, seg); err == nil {
		t.Fatal("empty segment accepted")
	}
}

func TestReadRejectsTruncation(t *testing.T) {
	seg := sampleSegment()
	var buf bytes.Buffer
	if err := Write(&buf, seg); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if _, _, err := Read(bytes.NewReader(raw[:len(raw)/2])); err == nil {
		t.Fatal("truncated file accepted")
	}
}
