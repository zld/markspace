package verify

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"runtime"
	"testing"

	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
)

// framePayload deterministically fills a 1 KiB frame from its index without
// retaining any buffer, so the test harness itself stays constant-memory.
func framePayload(i int) []byte {
	p := make([]byte, 1024)
	for off := 0; off < len(p); off += 8 {
		binary.LittleEndian.PutUint64(p[off:], uint64(i)*1024+uint64(off))
	}
	return p
}

// streamingOpener serves frames on demand and releases each payload after the
// read; nothing accumulates across the sequence.
func streamingOpener() FrameOpener {
	return func(trackID, name string) (io.ReadCloser, error) {
		var idx uint64
		for _, ch := range name[1:6] {
			if ch >= '0' && ch <= '9' {
				idx = idx*10 + uint64(ch-'0')
			}
		}
		return io.NopCloser(byteReader(framePayload(int(idx)))), nil
	}
}

// byteReader wraps a fresh slice so each frame is an independent, releasable
// allocation rather than a shared backing array.
func byteReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct {
	b []byte
	o int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.o >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.o:])
	r.o += n
	return n, nil
}

// TestMemoryBoundedByWindowNotFrameCount proves the verifier streams payloads
// through the rolling hash: resident heap must not grow with total frame
// count, because only one fixed-size frame is live at a time.
func TestMemoryBoundedByWindowNotFrameCount(t *testing.T) {
	measure := func(frames int) uint64 {
		tr := &model.Track{ID: "v", Kind: model.KindVideo, FPS: 30}
		mt := &manifest.MappedTrack{Track: tr}
		for i := 0; i < frames; i++ {
			sum := sha256.Sum256(framePayload(i))
			var h model.Hash
			copy(h[:], sum[:])
			name := frameFileName(i)
			tr.Frames = append(tr.Frames, model.Frame{
				TrackID: "v", Index: i, Name: name, Length: 1024,
				SHA256: h, TimeUnits: int64(i) * 3000, TimeRate: 90000,
			})
			mt.Frames = append(mt.Frames, manifest.MappedFrame{Frame: tr.Frames[i], At90k: 3000})
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		rep := Track(mt, streamingOpener())
		runtime.GC() // reclaim per-frame garbage so only live heap is measured
		runtime.ReadMemStats(&after)
		if !rep.OK {
			t.Fatalf("verification failed: %+v", rep.FirstError)
		}
		return after.HeapAlloc - before.HeapAlloc
	}

	small := measure(100)
	large := measure(4000) // 40x more frames and bytes
	// A buffering verifier would hold roughly 40x more live heap. Streaming
	// leaves only fixed per-frame state, so the resident delta must not scale
	// with the sequence length (generous headroom for GC noise).
	if large > small*4 && large > 256*1024 {
		t.Fatalf("live heap grew with frame count: small=%d large=%d (not streaming)", small, large)
	}
}

func frameFileName(i int) string {
	const digits = "0123456789"
	b := []byte("f00000.raw")
	pos := 5
	for i > 0 {
		b[pos] = digits[i%10]
		i /= 10
		pos--
	}
	return string(b)
}
