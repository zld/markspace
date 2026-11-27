// AAC payload handling. Two timing sources are supported, matching the
// delivery requirement:
//
//   - RTP (RFC 3640, mpeg4-generic): the AU-headers section gives each Access
//     Unit's size; an illegal header or a payload truncated mid-AU is located
//     precisely rather than skipped.
//   - MP4 sample table (handled in mp4.go): the stts/stsz boxes provide the
//     authoritative per-sample duration and size.
//
// All times remain integer nanoseconds.
package audio

import "mediasync/internal/model"

// AU is one Access Unit recovered from an RTP AAC packet.
type AU struct {
	Index  int // zero-based AU index within the packet
	Size   int // payload size in bytes
	Offset int // byte offset of the AU data inside the RTP payload
}

// ParseAUHeaders parses the AU-headers-length / AU-headers section of an
// RFC 3640 mpeg4-generic packet and bounds every AU against payloadLen.
//
// payload is the full RTP payload: 2-byte AU-headers-length, that many bits of
// AU headers, then the concatenated AAC access units.
func ParseAUHeaders(payload []byte, packetOffset int) ([]AU, *model.Failure) {
	if len(payload) < 2 {
		return nil, model.PtrFailure("", model.StageAAC, packetOffset, model.ErrTruncated,
			"AAC packet shorter than AU-headers-length")
	}
	headerBits := int(uint16(payload[0])<<8 | uint16(payload[1]))
	if headerBits%16 != 0 {
		return nil, model.PtrFailure("", model.StageAAC, packetOffset, model.ErrAUHeader,
			"AU-headers-length "+itoa(headerBits)+" is not a multiple of 16")
	}
	headerBytes := headerBits / 8
	if 2+headerBytes > len(payload) {
		return nil, model.PtrFailure("", model.StageAAC, packetOffset, model.ErrAUHeader,
			"AU headers claim "+itoa(headerBytes)+" bytes but payload is "+itoa(len(payload)-2))
	}

	count := headerBytes / 2
	aus := make([]AU, 0, count)
	dataCursor := 2 + headerBytes
	for i := 0; i < count; i++ {
		word := uint16(payload[2+2*i])<<8 | uint16(payload[3+2*i])
		size := int(word >> 3)
		if size <= 0 {
			return nil, model.PtrFailure("", model.StageAAC, packetOffset, model.ErrAUHeader,
				"AU["+itoa(i)+"] has non-positive size "+itoa(size))
		}
		if dataCursor+size > len(payload) {
			return nil, model.PtrFailure("", model.StageAAC, packetOffset, model.ErrAUTruncated,
				"AU["+itoa(i)+"] needs "+itoa(size)+" bytes at offset "+
					itoa(dataCursor)+" but payload ends at "+itoa(len(payload)))
		}
		aus = append(aus, AU{Index: i, Size: size, Offset: dataCursor})
		dataCursor += size
	}
	if len(aus) == 0 {
		return nil, model.PtrFailure("", model.StageAAC, packetOffset, model.ErrAUHeader,
			"no access units declared in AU headers")
	}
	return aus, nil
}

// SampleDurationNS converts an MP4 sample delta (in timescale units) to
// nanoseconds using exact integer arithmetic. A non-positive timescale or a
// result that overflows int64 is reported, never clamped silently.
func SampleDurationNS(delta, timescale uint32, packetOffset int) (model.NS, *model.Failure) {
	if timescale == 0 {
		return 0, model.PtrFailure("", model.StageMP4, packetOffset, model.ErrMP4Box,
			"zero media timescale")
	}
	// ns = delta * 1e9 / timescale. Split to keep the intermediate product in
	// range while staying exact: whole seconds plus the remainder.
	const e9 uint64 = 1_000_000_000
	d := uint64(delta)
	whole := d / uint64(timescale)
	rem := d % uint64(timescale)
	ns := whole*e9 + (rem*e9)/uint64(timescale)
	if ns > uint64(maxNS) {
		return 0, model.PtrFailure("", model.StageMP4, packetOffset, model.ErrDurationOverflow,
			"sample duration overflows nanosecond time base")
	}
	return model.NS(ns), nil
}

const maxNS = int64(1<<63 - 1)
