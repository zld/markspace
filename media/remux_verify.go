package media

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// verifyRemux re-opens the produced file and compares it against the input.
func verifyRemux(path string, src io.ReadSeeker, tracks []*TrackInfo, res *RemuxResult) error {
	f, err := os.Open(path)
	if err != nil {
		return WrapError(ClassIO, "verify.open", 0, err, "open remux output")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return WrapError(ClassIO, "verify.stat", 0, err, "stat remux output")
	}
	out, err := ProbeFile(f, fi.Size(), path)
	if err != nil {
		return NewError(ClassIntegrity, "verify.probe", 0, "output failed to re-probe: %v", err)
	}
	if len(out.Tracks) != len(tracks) {
		return NewError(ClassIntegrity, "verify.tracks", 0, "output has %d tracks, expected %d", len(out.Tracks), len(tracks))
	}
	if err := ValidateMovieBounds(out); err != nil {
		return NewError(ClassIntegrity, "verify.bounds", 0, "output bounds invalid: %v", err)
	}
	for i, ti := range tracks {
		got := out.Tracks[i]
		ev := &res.Tracks[i]
		if got.TrackID != uint32(i+1) {
			return NewError(ClassIntegrity, "verify.id", 0, "target track %d has id %d", i+1, got.TrackID)
		}
		if got.SampleCount != ti.SampleCount {
			return NewError(ClassIntegrity, "verify.count", 0, "track %d sample count %d != %d", i+1, got.SampleCount, ti.SampleCount).WithTrack(got.TrackID)
		}
		srcHash, err := hashSampleStream(src, ti.Table)
		if err != nil {
			return err
		}
		outHash, err := hashSampleStream(f, got.Table)
		if err != nil {
			return err
		}
		ev.SourceSHA, ev.OutputSHA = srcHash, outHash
		ev.ByteMatch = srcHash == outHash
		ev.DTSMatch = equalU64(ti.Table.DTS, got.Table.DTS)
		ev.CTSMatch = equalI64(ti.Table.CTS, got.Table.CTS)
		ev.SyncMatch = equalBool(ti.Table.Sync, got.Table.Sync)
		if !ev.ByteMatch || !ev.DTSMatch || !ev.CTSMatch || !ev.SyncMatch {
			return NewError(ClassIntegrity, "verify.samples", 0,
				"track %d roundtrip mismatch (bytes=%v dts=%v cts=%v sync=%v)",
				i+1, ev.ByteMatch, ev.DTSMatch, ev.CTSMatch, ev.SyncMatch).WithTrack(got.TrackID)
		}
	}
	return nil
}

func equalU64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalI64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalBool(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SampleHash computes a stable hash over ordered (dts,size,cts,sync) tuples.
// The resume logic uses it to detect when previously checked content changed.
func SampleHash(st *SampleTable) string {
	h := sha256.New()
	var b [24]byte
	for i := range st.Samples {
		s := st.Samples[i]
		putU64(b[0:8], s.DTS)
		putU64(b[8:16], uint64(s.Size))
		putI64(b[16:24], s.CTS)
		h.Write(b[:])
		if s.Sync {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func putU64(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}

func putI64(b []byte, v int64) {
	putU64(b, uint64(v))
}
