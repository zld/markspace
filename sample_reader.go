package mediacore

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// SampleReader streams sample payloads from the source file in file order.
// At most one chunk of payload is resident in memory; no method ever loads
// the complete media payload.
type SampleReader struct {
	f     *os.File
	track *Track
	// buffered reads use bufio with a fixed window; offset tracks the file
	// position of the next unread byte in the current sample.
	buf *bufio.Reader
}

// NewSampleReader opens path for streaming reads of t's samples.
func NewSampleReader(path string, t *Track) (*SampleReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, wrapErr(KindIO, "sample-reader", err)
	}
	return &SampleReader{f: f, track: t, buf: bufio.NewReaderSize(f, 64*1024)}, nil
}

// Close releases the file handle.
func (r *SampleReader) Close() error { return r.f.Close() }

// ReadSample writes sample i's payload to w and returns its size. The caller
// supplies the destination, so even large samples never have to be fully
// buffered here twice.
func (r *SampleReader) ReadSample(i int, w io.Writer) (int64, error) {
	if i < 0 || i >= len(r.track.Samples) {
		return 0, fail(KindIndex, "sample-reader", "sample %d out of range [0,%d)", i, len(r.track.Samples))
	}
	s := r.track.Samples[i]
	if s.Size == 0 {
		return 0, nil
	}
	if _, err := r.f.Seek(s.Offset, io.SeekStart); err != nil {
		return 0, wrapErr(KindIO, "sample-reader", err)
	}
	r.buf.Reset(r.f)
	n, err := io.CopyN(w, r.buf, s.Size)
	if err != nil {
		return n, wrapErr(KindIO, "sample-reader", err)
	}
	return n, nil
}

// SampleBytes reads one sample fully. It is intended for small samples and
// tests; the pipeline uses ReadSample.
func (r *SampleReader) SampleBytes(i int) ([]byte, error) {
	if i < 0 || i >= len(r.track.Samples) {
		return nil, fail(KindIndex, "sample-reader", "sample %d out of range [0,%d)", i, len(r.track.Samples))
	}
	s := r.track.Samples[i]
	data := make([]byte, s.Size)
	if s.Size == 0 {
		return data, nil
	}
	if _, err := r.f.Seek(s.Offset, io.SeekStart); err != nil {
		return nil, wrapErr(KindIO, "sample-reader", err)
	}
	if _, err := io.ReadFull(r.f, data); err != nil {
		return nil, wrapErr(KindIO, "sample-reader", err)
	}
	return data, nil
}

// SampleHash hashes one sample's payload without materialising it.
func (r *SampleReader) SampleHash(i int) (string, error) {
	s := r.track.Samples[i]
	if s.Size == 0 {
		return hex.EncodeToString(sha256.New().Sum(nil)), nil
	}
	if _, err := r.f.Seek(s.Offset, io.SeekStart); err != nil {
		return "", wrapErr(KindIO, "sample-reader", err)
	}
	r.buf.Reset(r.f)
	h := sha256.New()
	if _, err := io.CopyN(h, r.buf, s.Size); err != nil {
		return "", wrapErr(KindIO, "sample-reader", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ByteSize returns the sum of sample sizes.
func (t *Track) ByteSize() int64 {
	var n int64
	for _, s := range t.Samples {
		n += s.Size
	}
	return n
}
