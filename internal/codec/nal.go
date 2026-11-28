// Package codec parses H.264, H.265 and AV1 bare streams into units
// and reports the first offending byte offset for truncated units and
// illegal headers.
package codec

import "mediacheck/internal/fault"

// Kind identifies an elementary stream codec.
type Kind int

const (
	H264 Kind = iota
	H265
	AV1
)

func (k Kind) String() string {
	switch k {
	case H264:
		return "h264"
	case H265:
		return "h265"
	case AV1:
		return "av1"
	}
	return "unknown"
}

// ParseKind maps a CLI name to a Kind.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "h264":
		return H264, nil
	case "h265":
		return H265, nil
	case "av1":
		return AV1, nil
	}
	return 0, fault.New(fault.ErrBadHeader, fault.NoOffset, "unknown codec %q", s)
}

// Unit is one parsed elementary stream unit (NAL unit or OBU).
type Unit struct {
	Offset    int64 // byte offset of the unit header in the stream
	StartCode int64 // byte offset of the start prefix (== Offset for AV1)
	Size      int   // bytes from header to end of payload
	Type      uint8 // codec-specific unit type
	Name      string
	Keyframe  bool
}

// Units parses data into units for the given codec kind.
func Units(data []byte, k Kind) ([]Unit, error) {
	if k == AV1 {
		return parseAV1(data)
	}
	return parseAnnexB(data, k)
}

// parseAnnexB scans 0x000001 / 0x00000001 start codes and validates
// the 1-byte (H.264) or 2-byte (H.265) unit headers.
func parseAnnexB(data []byte, k Kind) ([]Unit, error) {
	var units []Unit
	i := 0
	for i < len(data) {
		sc := startCodeLen(data[i:])
		if sc == 0 {
			i++
			continue
		}
		hdr := i + sc
		need := 1
		if k == H265 {
			need = 2
		}
		if hdr+need > len(data) {
			return nil, fault.New(fault.ErrTruncatedUnit, int64(len(data)),
				"start code at %d without complete %d-byte header", i, need)
		}
		u, err := annexBHeader(data[hdr:], hdr, k)
		if err != nil {
			return nil, err
		}
		u.StartCode = int64(i)
		// payload runs to the next start code or EOF
		end := len(data)
		for j := hdr + need; j+2 < len(data); j++ {
			if data[j] == 0 && data[j+1] == 0 && (data[j+2] == 1 ||
				(j+3 < len(data) && data[j+2] == 0 && data[j+3] == 1)) {
				end = j
				break
			}
		}
		u.Size = end - hdr
		units = append(units, u)
		i = end
	}
	return units, nil
}

func startCodeLen(b []byte) int {
	if len(b) >= 3 && b[0] == 0 && b[1] == 0 && b[2] == 1 {
		return 3
	}
	if len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 1 {
		return 4
	}
	return 0
}

func annexBHeader(b []byte, off int, k Kind) (Unit, error) {
	o := int64(off)
	if b[0]&0x80 != 0 {
		return Unit{}, fault.New(fault.ErrBadHeader, o, "forbidden_zero_bit set")
	}
	if k == H264 {
		typ := b[0] & 0x1F
		if typ == 0 || typ > 23 {
			return Unit{}, fault.New(fault.ErrBadHeader, o, "reserved nal_unit_type %d", typ)
		}
		return Unit{Offset: o, Type: typ, Name: h264Name(typ), Keyframe: typ == 5}, nil
	}
	// H.265: 2-byte header, nuh_temporal_id_plus1 must be >= 1
	typ := (b[0] >> 1) & 0x3F
	if b[1]&0x07 == 0 {
		return Unit{}, fault.New(fault.ErrBadHeader, o, "nuh_temporal_id_plus1 is zero")
	}
	if typ > 40 {
		return Unit{}, fault.New(fault.ErrBadHeader, o, "reserved nal_unit_type %d", typ)
	}
	return Unit{Offset: o, Type: typ, Name: h265Name(typ), Keyframe: typ == 19 || typ == 20}, nil
}

func h264Name(t uint8) string {
	switch t {
	case 1:
		return "slice"
	case 5:
		return "IDR"
	case 6:
		return "SEI"
	case 7:
		return "SPS"
	case 8:
		return "PPS"
	case 9:
		return "AUD"
	case 12:
		return "filler"
	}
	return "nal"
}

func h265Name(t uint8) string {
	switch t {
	case 0, 1:
		return "trail"
	case 19, 20:
		return "IDR"
	case 32:
		return "VPS"
	case 33:
		return "SPS"
	case 34:
		return "PPS"
	case 38:
		return "filler"
	case 39:
		return "SEI"
	}
	return "nal"
}
