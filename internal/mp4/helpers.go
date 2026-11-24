package mp4

import (
	"errors"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
)

func wrapParse(err error, offset int64, track uint32) error {
	code := clock.CodeBadManifest
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		code = clock.CodeTruncatedTable
	}
	msg := err.Error()
	if containsAny(msg, "too large", "not enough bits", "overrun", "invalid alignment") {
		code = clock.CodeTruncatedTable
	}
	pe := clock.New(clock.StageScan, code, "%v", err).WithCause(err)
	if offset >= 0 {
		pe = pe.WithOffset(offset)
	}
	if track != 0 {
		pe = pe.WithTrack(track)
	}
	return pe
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && indexOfSub(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOfSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func beU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func beU64(b []byte) uint64 {
	return uint64(beU32(b[0:4]))<<32 | uint64(beU32(b[4:8]))
}

// p builds a box path from four-character type codes.
func p(parts ...string) gomp4.BoxPath {
	path := make(gomp4.BoxPath, len(parts))
	for i, s := range parts {
		path[i] = gomp4.StrToBoxType(s)
	}
	return path
}
