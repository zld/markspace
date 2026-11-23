package media

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// buildSampleTable expands stts/ctts/stsc/stsz/stco(st)/stss into per-sample rows.
// It is defined in sample.go alongside the validation that mirrors it.

// HashSegment streams the compressed bytes of a segment through SHA-256 in
// sample (decode) order without buffering the whole segment.
func HashSegment(r io.ReadSeeker, st *SampleTable, seg Segment) (string, error) {
	if seg.First+seg.Count > st.Count {
		return "", NewError(ClassInvalidSample, "hash.segment", 0, "segment exceeds sample count").WithTrack(st.TrackID).WithSegment(seg.Index)
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for i := uint32(0); i < seg.Count; i++ {
		s := st.Samples[seg.First+i]
		if _, err := r.Seek(int64(s.Offset), io.SeekStart); err != nil {
			return "", WrapError(ClassIO, "hash.seek", int64(s.Offset), err, "seek sample %d", s.Index).WithTrack(st.TrackID)
		}
		if _, err := io.CopyBuffer(h, io.LimitReader(r, int64(s.Size)), buf); err != nil {
			return "", WrapError(ClassIO, "hash.read", int64(s.Offset), err, "read sample %d", s.Index).WithTrack(st.TrackID)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile returns the SHA-256 of an entire readable stream from its start.
func HashFile(r io.ReadSeeker) (string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", WrapError(ClassIO, "hashfile.seek", 0, err, "rewind failed")
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(r, MaxInputSize+1)); err != nil {
		return "", WrapError(ClassIO, "hashfile.read", 0, err, "read failed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
