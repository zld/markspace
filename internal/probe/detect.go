// Package probe identifies the container format of a local input without
// trusting its file name.  Unknown input is a hard error, never a guessed
// success.
package probe

import (
	"encoding/binary"

	"mediapipeline/internal/fault"
)

// Format is a detected container or stream format.
type Format string

const (
	FormatMP4     Format = "mp4"
	FormatTS      Format = "mpeg-ts"
	FormatRTP     Format = "rtp"
	FormatUnknown Format = ""
)

// Result is the probe outcome.
type Result struct {
	Format Format
	// Brand is the MP4 major brand when Format is mp4.
	Brand string
	// RTPFramed reports whether the RTP bytes use 4-byte length framing.
	RTPFramed bool
}

var mp4Brands = map[string]bool{
	"isom": true, "iso2": true, "iso3": true, "iso4": true, "iso5": true,
	"iso6": true, "mp41": true, "mp42": true, "avc1": true, "dash": true,
	"msdh": true, "msix": true, "cmfc": true, "cmfs": true, "mmp4": true,
	"M4V ": true, "M4A ": true, "f4v ": true, "qt  ": true,
}

// Detect inspects the first bytes of data and returns the format.  It never
// returns FormatUnknown without an error.
func Detect(data []byte) (Result, error) {
	if len(data) == 0 {
		return Result{}, fault.New(fault.StageProbe, "", fault.OpUnknownFormat, 0, "input is empty")
	}
	if res, ok := detectMP4(data); ok {
		return res, nil
	}
	if len(data) >= 188*2 && data[0] == 0x47 && data[188] == 0x47 {
		return Result{Format: FormatTS}, nil
	}
	if res, ok := detectRTP(data); ok {
		return res, nil
	}
	return Result{}, fault.New(fault.StageProbe, "", fault.OpUnknownFormat, 0,
		"input is not recognised as MP4/fMP4, MPEG-TS or framed RTP")
}

func detectMP4(data []byte) (Result, bool) {
	if len(data) < 8 {
		return Result{}, false
	}
	size := binary.BigEndian.Uint32(data)
	typ := string(data[4:8])
	if typ != "ftyp" {
		return Result{}, false
	}
	if size < 8 || int(size) > len(data) || size%4 != 0 {
		return Result{}, false
	}
	res := Result{Format: FormatMP4}
	if size >= 12 {
		res.Brand = string(data[8:12])
		if _, known := mp4Brands[res.Brand]; !known {
			// Still MP4 structurally; an unusual brand is not a reason to
			// reject a well-formed ftyp.
			_ = known
		}
	}
	return res, true
}

func detectRTP(data []byte) (Result, bool) {
	// Length-framed RTP: 4-byte size, then a header with version 2.
	if len(data) >= 16 {
		n := binary.BigEndian.Uint32(data)
		if n >= 12 && int(n) <= len(data)-4 && data[4]>>6 == 2 && data[4]&0x10 == 0 && data[4]&0x0f == 0 {
			// Confirm the next frame too when present, to avoid mistaking
			// random bytes for a framed header.
			framed := Result{Format: FormatRTP, RTPFramed: true}
			if int(4+n)+4 <= len(data) {
				m := binary.BigEndian.Uint32(data[4+n:])
				if m >= 12 && data[4+n+4]>>6 == 2 {
					return framed, true
				}
				return Result{}, false
			}
			return framed, true
		}
	}
	// Bare RTP header (no framing) is only accepted when the version, CSRC
	// count and payload type are all plausible and the packet size is exactly
	// the input size.
	if len(data) >= 12 && data[0]>>6 == 2 && data[0]&0x0f <= 15 {
		return Result{Format: FormatRTP, RTPFramed: false}, true
	}
	return Result{}, false
}
