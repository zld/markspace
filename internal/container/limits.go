package container

import (
	"math"

	"mediasync/internal/fault"
)

// Safety limits. They are deliberately generous for offline media but cap
// memory and table sizes so a malformed count field cannot exhaust resources.
const (
	MaxFileSize   = 512 << 20 // 512 MiB
	MaxTracks     = 64
	MaxTableCount = 1_000_000 // samples/chunks/table entries per track
)

// addOffset adds delta to base, rejecting overflow and values past fileSize.
func addOffset(base, delta, fileSize uint64) (uint64, error) {
	if delta > math.MaxUint64-base {
		return 0, fault.New(fault.ErrOverflow, "", int64(base),
			"offset %d + %d overflows uint64", base, delta)
	}
	end := base + delta
	if end > fileSize {
		return 0, fault.New(fault.ErrOutOfBounds, "", int64(base),
			"range %d+%d=%d exceeds file size %d", base, delta, end, fileSize)
	}
	return end, nil
}

func checkedAdd(a, b uint64) (uint64, bool) {
	if b > ^uint64(0)-a {
		return 0, false
	}
	return a + b, true
}

func checkedMul(a, b uint32) (uint64, bool) {
	p := uint64(a) * uint64(b)
	if a != 0 && p/uint64(a) != uint64(b) {
		return 0, false
	}
	return p, true
}
