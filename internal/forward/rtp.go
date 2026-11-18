// Package forward implements the bounded RTP relay queue, deterministic
// packet validation and the loss/ordering/duplication fault injection.
package forward

import (
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// RTP clock rate used by the deterministic audio fixtures (8 kHz).
const DefaultClockRate uint32 = 8000

// ValidationFailure categorises malformed packets.
type ValidationFailure struct {
	Reason string
	Bytes  int
}

func (e *ValidationFailure) Error() string {
	return fmt.Sprintf("malformed RTP packet (%d bytes): %s", e.Bytes, e.Reason)
}

// Common validation errors, wrapped by ValidationFailure at call sites.
var (
	errTooShort   = errors.New("shorter than the 12-byte fixed header")
	errVersion    = errors.New("RTP version must be 2")
	errHeader     = errors.New("header unmarshal failed")
	errPayload    = errors.New("declared header exceeds packet length")
	errBadExt     = errors.New("extension header length inconsistent")
)

// Parse validates the wire bytes and returns the parsed packet without
// mutating any media field. The caller retains the original bytes for
// forwarding, guaranteeing SSRC/sequence/timestamp/extensions are preserved.
func Parse(buf []byte) (*rtp.Packet, error) {
	if len(buf) < 12 {
		return nil, &ValidationFailure{Reason: errTooShort.Error(), Bytes: len(buf)}
	}
	if buf[0]>>6 != 2 {
		return nil, &ValidationFailure{Reason: errVersion.Error(), Bytes: len(buf)}
	}
	p := &rtp.Packet{}
	if err := p.Unmarshal(buf); err != nil {
		return nil, &ValidationFailure{Reason: errHeader.Error() + ": " + err.Error(), Bytes: len(buf)}
	}
	// Re-marshal length check: an inconsistent extension profile/length or
	// truncated payload must never slip through silently.
	if got := p.MarshalSize(); got > len(buf) {
		return nil, &ValidationFailure{Reason: errPayload.Error(), Bytes: len(buf)}
	}
	if p.Header.Extension && !extensionConsistent(buf, &p.Header) {
		return nil, &ValidationFailure{Reason: errBadExt.Error(), Bytes: len(buf)}
	}
	return p, nil
}

// extensionConsistent double-checks the 4-octet extension header against the
// raw buffer so that a crafted packet with a lying extension length is caught.
func extensionConsistent(buf []byte, h *rtp.Header) bool {
	csrc := 4 * len(h.CSRC)
	off := 12 + csrc
	if off+4 > len(buf) {
		return false
	}
	words := int(buf[off+2])<<8 | int(buf[off+3])
	return off+4+4*words <= len(buf)
}

// SeqDelta returns the signed forward distance from a to b in RTP sequence
// number space (mod 2^16), handling wraparound. Positive means b is newer.
func SeqDelta(a, b uint16) int32 {
	d := int32(b - a)
	if d > 32767 {
		d -= 65536
	}
	if d < -32768 {
		d += 65536
	}
	return d
}

// TSDelta returns the signed forward distance between two RTP timestamps
// (mod 2^32), handling timestamp wraparound.
func TSDelta(a, b uint32) int64 {
	d := int64(b - a)
	if d > 1<<31 {
		d -= 1 << 32
	}
	if d < -1<<31 {
		d += 1 << 32
	}
	return d
}

// EqualMediaFields reports whether the media identity of two packets matches.
// Used to prove the relay never rewrites SSRC/seq/timestamp/marker or the
// extension profile. Extension payload bytes are compared via Marshal since
// pion/rtp keeps the Extension fields unexported.
func EqualMediaFields(x, y *rtp.Packet) bool {
	if x == nil || y == nil {
		return false
	}
	hx, hy := x.Header, y.Header
	if hx.SSRC != hy.SSRC || hx.SequenceNumber != hy.SequenceNumber ||
		hx.Timestamp != hy.Timestamp || hx.Marker != hy.Marker ||
		hx.Extension != hy.Extension || hx.ExtensionProfile != hy.ExtensionProfile ||
		hx.PayloadType != hy.PayloadType || hx.Padding != hy.Padding {
		return false
	}
	if len(hx.Extensions) != len(hy.Extensions) {
		return false
	}
	bx, errx := hx.Marshal()
	by, erry := hy.Marshal()
	if errx != nil || erry != nil {
		return false
	}
	return string(bx) == string(by)
}
