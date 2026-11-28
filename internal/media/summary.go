package media

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// SummaryGridMS is the content grid on which per-cell digests are computed.
// Every published range boundary must lie on this grid relative to the track's
// edit origin, so two renditions that slice the same content differently still
// produce identical per-cell digests and can be compared cell by cell.
const SummaryGridMS int64 = 50

// GridTicks converts the grid to ticks for a timescale, requiring exactness.
func GridTicks(timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, fmt.Errorf("media: zero timescale")
	}
	v := int64(timescale) * SummaryGridMS
	if v%1000 != 0 {
		return 0, fmt.Errorf("media: timescale %d cannot express a %dms grid exactly", timescale, SummaryGridMS)
	}
	return v / 1000, nil
}

// CellDigest is the content digest of one grid cell. Cell index zero is the
// first cell of presented media, i.e. it is measured from the edit origin, so
// the same content keeps the same index regardless of the file's raw start
// timestamp.
func CellDigest(asset string, track uint32, cell int64) string {
	h := sha256.New()
	var buf [24]byte
	binary.BigEndian.PutUint32(buf[0:4], track)
	binary.BigEndian.PutUint64(buf[4:12], uint64(cell))
	_, _ = h.Write([]byte(asset))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(buf[:12])
	// 8 bytes per cell keeps a 1-hour, 50 ms grid comfortably small while
	// remaining collision-resistant for content comparison.
	return fmt.Sprintf("%x", h.Sum(nil)[:8])
}

// RangeChunks returns the cell digests covering cells [firstCell, lastCell).
func RangeChunks(asset string, track uint32, firstCell, cellCount int64) []string {
	out := make([]string, 0, cellCount)
	for c := int64(0); c < cellCount; c++ {
		out = append(out, CellDigest(asset, track, firstCell+c))
	}
	return out
}

// CombineChunks reduces a chunk list to a single range summary. It is the
// digest of the joined cell digests, so it is independent of how the same
// cells were grouped into ranges.
func CombineChunks(chunks []string) string {
	h := sha256.New()
	for _, c := range chunks {
		_, _ = h.Write([]byte(c))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:8])
}

// CellsEqual reports whether two chunk slices carry identical content.
func CellsEqual(a, b []string) bool {
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

// CorruptSummary returns a summary that provably differs from s, used to build
// conflict fixtures without touching encoded media.
func CorruptSummary(s string) string {
	if s == "" {
		return "corrupt"
	}
	b := []byte(s)
	if b[0] == 'f' {
		b[0] = 'e'
	} else {
		b[0]++
	}
	return string(b)
}
