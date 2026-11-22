package index

import (
	"bytes"
	"encoding/json"
	"testing"

	"mediacore/media"
)

// benchmarkStream builds n 64x64 records with metadata under 256 bytes.
func benchmarkStream(b *testing.B, n int) []byte {
	b.Helper()
	frames := make([]*media.Frame, n)
	for i := 0; i < n; i++ {
		meta, _ := json.Marshal(media.Meta{VolumeDBFS: floatPtr(-20)})
		f := &media.Frame{
			Seq:           uint32(i),
			RTPTimestamp:  uint32(i * 3600),
			CaptureTimeMS: int64(i) * 40,
			Width:         64,
			Height:        64,
			DurationTicks: 3600,
			StreamID:      1,
			Payload:       make([]byte, 64*64),
			MetaRaw:       meta,
		}
		frames[i] = f
	}
	data, err := media.EncodeStream(frames)
	if err != nil {
		b.Fatal(err)
	}
	return data
}

func floatPtr(v float64) *float64 { return &v }

func BenchmarkScanSmallFrames(b *testing.B) {
	const frames = 20000
	data := benchmarkStream(b, frames)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx, err := Scan(bytes.NewReader(data), DefaultOptions())
		if err != nil {
			b.Fatal(err)
		}
		if idx.EntryCount != frames {
			b.Fatalf("entries=%d", idx.EntryCount)
		}
	}
	b.StopTimer()
	// Report throughput directly so it is visible without -benchmem decoding.
	b.ReportMetric(float64(frames)*float64(b.N)/b.Elapsed().Seconds(), "records/sec")
}

func BenchmarkScanSmallFramesParallel(b *testing.B) {
	const frames = 20000
	data := benchmarkStream(b, frames)
	b.SetBytes(int64(len(data)))
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			idx, err := Scan(bytes.NewReader(data), DefaultOptions())
			if err != nil {
				b.Fatal(err)
			}
			if idx.EntryCount != frames {
				b.Fatalf("entries=%d", idx.EntryCount)
			}
		}
	})
}
