package mp4wrap

import (
	"bytes"
	"testing"
)

func TestWriteReadRoundTripH264(t *testing.T) {
	samples := []Sample{
		{Data: []byte{0x65, 1, 2, 3, 4}, KeyFrame: true, Duration: 3600},
		{Data: []byte{0x41, 5, 6}, KeyFrame: false, Duration: 3600},
		{Data: []byte{0x41, 7, 8, 9}, KeyFrame: false, Duration: 3600},
		{Data: []byte{0x65, 10}, KeyFrame: true, Duration: 3600},
	}
	buf := &seekableBuffer{}
	if err := Write(buf, "H264", 90000, samples); err != nil {
		t.Fatal(err)
	}
	res, err := Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Codec != "H264" || res.Timescale != 90000 {
		t.Fatalf("header wrong: %+v", res)
	}
	if len(res.Samples) != len(samples) {
		t.Fatalf("sample count = %d, want %d", len(res.Samples), len(samples))
	}
	for i, want := range samples {
		got := res.Samples[i]
		if !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("sample %d bytes = %v, want %v", i, got.Data, want.Data)
		}
		if got.KeyFrame != want.KeyFrame || got.Duration != want.Duration {
			t.Fatalf("sample %d flags/duration wrong: %+v", i, got)
		}
	}
	keys := 0
	for _, s := range res.Samples {
		if s.KeyFrame {
			keys++
		}
	}
	if keys != 2 {
		t.Fatalf("key count = %d, want 2", keys)
	}
}

func TestWriteReadRoundTripH265(t *testing.T) {
	samples := []Sample{
		{Data: []byte{0x26, 1, 2}, KeyFrame: true, Duration: 3000},
		{Data: []byte{0x02, 3}, KeyFrame: false, Duration: 3000},
	}
	buf := &seekableBuffer{}
	if err := Write(buf, "H265", 90000, samples); err != nil {
		t.Fatal(err)
	}
	res, err := Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Codec != "H265" || len(res.Samples) != 2 {
		t.Fatalf("HEVC container wrong: %+v", res)
	}
	if !bytes.Equal(res.Samples[0].Data, samples[0].Data) {
		t.Fatalf("HEVC sample bytes lost: %v", res.Samples[0].Data)
	}
}

func TestWriteIsDeterministic(t *testing.T) {
	samples := []Sample{{Data: []byte{1, 2}, KeyFrame: true, Duration: 100}}
	a := &seekableBuffer{}
	b := &seekableBuffer{}
	if err := Write(a, "H264", 90000, samples); err != nil {
		t.Fatal(err)
	}
	if err := Write(b, "H264", 90000, samples); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("MP4 output for identical inputs must be byte-stable")
	}
}

// seekableBuffer gives the box writer an io.WriteSeeker over memory.
type seekableBuffer struct {
	data []byte
	pos  int64
}

func (b *seekableBuffer) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if int(end) > len(b.data) {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	}
	copy(b.data[b.pos:end], p)
	b.pos = end
	return len(p), nil
}

func (b *seekableBuffer) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case 0:
		next = offset
	case 1:
		next = b.pos + offset
	case 2:
		next = int64(len(b.data)) + offset
	}
	if next < 0 {
		return 0, bytes.ErrTooLarge
	}
	if int(next) > len(b.data) {
		grown := make([]byte, next)
		copy(grown, b.data)
		b.data = grown
	}
	b.pos = next
	return next, nil
}

func (b *seekableBuffer) Bytes() []byte { return b.data }
