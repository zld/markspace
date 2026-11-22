package index

import (
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"os"

	"mediacore/internal/clock"
)

// streamWriter serializes header + fixed-width records while continuously
// hashing the bytes; Finalize appends the trailer hash. Only one record-sized
// buffer is ever live, so build memory is independent of sample count.
type streamWriter struct {
	w      io.Writer
	h      hash.Hash
	buf    [recordSize]byte
	count  uint32
	header Header
}

func newStreamWriter(w io.Writer, h Header) (*streamWriter, error) {
	sw := &streamWriter{w: w, h: sha256.New(), header: h}
	hb := make([]byte, headerSize)
	encodeHeader(h, hb)
	if err := sw.writeRaw(hb); err != nil {
		return nil, err
	}
	sw.h.Write(hb)
	return sw, nil
}

func (s *streamWriter) writeRaw(p []byte) error {
	if _, err := s.w.Write(p); err != nil {
		return clock.ErrRecoverablef("index.write", "%v", err)
	}
	return nil
}

func (s *streamWriter) Append(r Record) error {
	if s.count >= s.header.SampleCount {
		return clock.ErrInvariantf("index.write", "record overflow beyond header count %d", s.header.SampleCount)
	}
	r.Encode(s.buf[:])
	if _, err := s.w.Write(s.buf[:]); err != nil {
		return clock.ErrRecoverablef("index.write", "%v", err)
	}
	s.h.Write(s.buf[:])
	s.count++
	return nil
}

func (s *streamWriter) Finalize() (string, error) {
	if s.count != s.header.SampleCount {
		return "", clock.ErrInvariantf("index.write", "finalize with %d/%d records", s.count, s.header.SampleCount)
	}
	sum := s.h.Sum(nil)
	if _, err := s.w.Write(sum); err != nil {
		return "", clock.ErrRecoverablef("index.write", "trailer: %v", err)
	}
	return hexEncode(sum), nil
}

func hexEncode(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[2*i] = hexd[v>>4]
		out[2*i+1] = hexd[v&0xf]
	}
	return string(out)
}

// CacheStatus summarizes a cache-load attempt for the demo report.
type CacheStatus struct {
	State  string // "hit" or "rebuilt"
	Reason string // why a rebuild was required, empty on hit
	Header Header
}

// LoadOrVerify checks the cache trailer, magic, schema version, input digest
// and sample count. Any mismatch (including truncation) returns a classified
// recoverable error plus the reason, which the runner turns into a rebuild.
func LoadOrVerify(path string, wantInputDigest string, wantTrackID, wantCount uint32) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, clock.ErrRecoverablef("index.cache", "open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Header{}, clock.ErrRecoverablef("index.cache", "stat: %v", err)
	}
	wantSize := ExpectedSize(wantCount)
	if fi.Size() != wantSize {
		return Header{}, clock.ErrRecoverablef("index.cache", "size %d != expected %d (truncated or stale)", fi.Size(), wantSize)
	}

	// Hash everything except the 32-byte trailer.
	h := sha256.New()
	if _, err := io.CopyN(h, f, fi.Size()-trailerSize); err != nil {
		return Header{}, clock.ErrRecoverablef("index.cache", "read body: %v", err)
	}
	trailer := make([]byte, trailerSize)
	if _, err := io.ReadFull(f, trailer); err != nil {
		return Header{}, clock.ErrRecoverablef("index.cache", "read trailer: %v", err)
	}
	if got := h.Sum(nil); !constantEq(got, trailer) {
		return Header{}, clock.ErrRecoverablef("index.cache", "trailer digest mismatch")
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Header{}, clock.ErrRecoverablef("index.cache", "seek: %v", err)
	}
	hb := make([]byte, headerSize)
	if _, err := io.ReadFull(f, hb); err != nil {
		return Header{}, clock.ErrRecoverablef("index.cache", "read header: %v", err)
	}
	hdr, err := decodeHeader(hb)
	if err != nil {
		return Header{}, err
	}
	if hdr.SchemaVersion != SchemaVersion {
		return hdr, clock.ErrRecoverablef("index.cache", "schema %d != supported %d", hdr.SchemaVersion, SchemaVersion)
	}
	if hdr.TrackID != wantTrackID || hdr.SampleCount != wantCount {
		return hdr, clock.ErrRecoverablef("index.cache", "track/count mismatch")
	}
	if got := hexEncode(hdr.InputDigest[:]); got != wantInputDigest {
		return hdr, clock.ErrRecoverablef("index.cache", "input digest mismatch: cache=%s want=%s", got[:16], wantInputDigest[:16])
	}
	return hdr, nil
}

// ReadRecord performs an O(1) fixed-width lookup. Bounds are checked against
// the header count before any seek.
func ReadRecord(f *os.File, h Header, index uint32) (Record, error) {
	if index >= h.SampleCount {
		return Record{}, clock.ErrInvalidArgument("index: record %d out of range [0,%d)", index, h.SampleCount)
	}
	off := int64(headerSize) + int64(index)*recordSize
	buf := make([]byte, recordSize)
	if _, err := f.ReadAt(buf, off); err != nil {
		return Record{}, clock.ErrRecoverablef("index.read", "record %d: %v", index, err)
	}
	return Decode(buf), nil
}

// ReadAllRecords is used by tests and the deterministic digest check; the
// runner itself accesses records one batch at a time via ReadRecord.
func ReadAllRecords(f *os.File, h Header) ([]Record, error) {
	out := make([]Record, h.SampleCount)
	for i := range out {
		r, err := ReadRecord(f, h, uint32(i))
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

// IsCacheMiss reports whether an error represents a missing/broken cache
// (safe to rebuild) rather than an unrelated failure.
func IsCacheMiss(err error) bool {
	var me *clock.MediaError
	return errors.As(err, &me) && me.Kind == clock.KindRecoverable
}

func constantEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
