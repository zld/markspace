package session

import (
	"fmt"

	"avcheck/internal/media"
)

func mediaErr(format string, args ...any) error {
	return media.NewError(media.KindRange, 0, 0, -1, format, args...)
}

// corruptPayload flips one bit inside the RTP payload region. The metadata
// extension precedes the payload, so the checksum no longer matches while the
// packet stays parseable.
func corruptPayload(raw []byte) []byte {
	out := append([]byte(nil), raw...)
	if len(out) < 16 {
		return out
	}
	pos := len(out) - 1 - len(out)/3
	if pos < 12 {
		pos = 12
	}
	out[pos] ^= 0x40
	return out
}

// truncatePacket drops trailing payload bytes, leaving a parseable packet
// shorter than its declared metadata length.
func truncatePacket(raw []byte) []byte {
	if len(raw) < 20 {
		return raw
	}
	cut := len(raw) / 4
	if cut < 1 {
		cut = 1
	}
	return append([]byte(nil), raw[:len(raw)-cut]...)
}

// String renders the realized tally for logs.
func (c FaultCounts) String() string {
	return fmt.Sprintf("dup=%d reorder=%d lost=%d corrupt=%d trunc=%d",
		c.Duplicated, c.Reordered, c.Lost, c.Corrupted, c.Truncated)
}
