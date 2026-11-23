package hls

import (
	"strconv"
	"strings"

	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
)

// BuildPlaylist synthesizes a deterministic media playlist for one fixture
// representation. A discontinuity is inserted between segments 3 and 4 so the
// reset path is always exercised; late/duplicate faults add matching entries.
func BuildPlaylist(r *fixture.Representation, faults []model.FaultKind) string {
	has := func(k model.FaultKind) bool {
		for _, f := range faults {
			if f == k {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-TARGETDURATION:1\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	for i, seg := range r.Segments {
		if i == 4 {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		// Exact decimal duration derived from the integer timescale.
		secs := exactDecimal(seg.Duration, r.Timescale)
		b.WriteString("#EXTINF:" + secs + ",\n")
		b.WriteString(segmentURI(r.ID, seg.Sequence) + "\n")
		if has(model.FaultDuplicate) && i == 2 {
			// Re-listed URI: the parser must flag a duplicate.
			b.WriteString("#EXTINF:" + secs + ",\n")
			b.WriteString(segmentURI(r.ID, seg.Sequence) + "\n")
		}
		if has(model.FaultLate) && i == 5 {
			// A late re-announcement carrying the same sequence number.
			b.WriteString("#EXTINF:" + secs + ",\n")
			b.WriteString(segmentURI(r.ID, seg.Sequence) + "?late=1\n")
		}
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func segmentURI(id string, seq uint32) string {
	return id + "/seg-" + strconv.FormatUint(uint64(seq), 10) + ".m4s"
}

// exactDecimal renders duration/timescale as an exact decimal string without
// floating point, e.g. 10000/25000 -> "0.4".
func exactDecimal(duration, timescale int64) string {
	whole := duration / timescale
	rem := duration % timescale
	if rem == 0 {
		return strconv.FormatInt(whole, 10) + ".0"
	}
	// Scale the remainder to at most 6 fractional digits, trimming zeros.
	digits := make([]byte, 0, 6)
	num := rem
	for i := 0; i < 6; i++ {
		num *= 10
		digits = append(digits, byte('0'+num/timescale))
		num %= timescale
		if num == 0 {
			break
		}
	}
	return strconv.FormatInt(whole, 10) + "." + string(digits)
}
