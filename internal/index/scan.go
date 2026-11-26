package index

import (
	"fmt"

	"liveregress/internal/fault"
)

// Codec identifies the bitstream syntax scanned for sync markers.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecH265 Codec = "h265"
	CodecAAC  Codec = "aac"
	CodecPCM  Codec = "pcm"
)

// SampleRef locates one sample and its presentation time.
type SampleRef struct {
	Time      int64
	Offset    int64
	Size      uint32
	TrackID   uint32
	IsKeyHint bool // from container flags, if known
}

// SampleReader returns the raw bytes of a sample.
type SampleReader func(SampleRef) ([]byte, error)

// ScanSample decides whether a sample begins with a sync/key frame for the
// given codec. For PCM and AAC every sample is a sync sample. Unknown codecs
// fail rather than guessing.
func ScanSample(codec Codec, data []byte) (bool, error) {
	switch codec {
	case CodecPCM, CodecAAC:
		return true, nil
	case CodecH264:
		return scanAnnexB(data, false), nil
	case CodecH265:
		return scanAnnexB(data, true), nil
	default:
		return false, fault.New(fault.KindUnsupported, "", -1,
			fmt.Sprintf("no sync-marker scanner for codec %q", codec)).Recover(false)
	}
}

// Rebuild reconstructs keyframe facts by scanning sample bytes, ignoring any
// container hint. This is the fallback when an I-frame index is absent.
func Rebuild(layer string, timescale uint32, codec Codec, samples []SampleRef, read SampleReader) (*Index, error) {
	var facts []Keyframe
	for _, s := range samples {
		data, err := read(s)
		if err != nil {
			return nil, err
		}
		key, err := ScanSample(codec, data)
		if err != nil {
			return nil, err
		}
		if key {
			facts = append(facts, Keyframe{Time: s.Time, Offset: s.Offset, Size: s.Size, TrackID: s.TrackID})
		}
	}
	if len(facts) == 0 {
		return nil, fault.New(fault.KindIndex, "", -1,
			"rebuild found no keyframes in "+layer).Recover(false)
	}
	return BuildFromFacts(layer, timescale, "rebuilt", facts), nil
}

// scanAnnexB reports whether the access unit contains an IDR (H.264) or an
// IRAP picture (H.265) NAL unit.
func scanAnnexB(data []byte, hevc bool) bool {
	for i := 0; i+3 <= len(data); i++ {
		var prefix int
		if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			prefix = 4
		} else if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			prefix = 3
		}
		if prefix == 0 {
			continue
		}
		h := i + prefix
		if h >= len(data) {
			return false
		}
		if hevc {
			if data[h]&0x80 != 0 {
				return false // forbidden_zero_bit must be zero
			}
			t := (data[h] >> 1) & 0x3F
			// IRAP pictures: BLA (16-18), IDR (19-20), CRA (21).
			if t >= 16 && t <= 21 {
				return true
			}
		} else {
			t := data[h] & 0x1F
			if t == 5 { // IDR
				return true
			}
		}
		i = h
	}
	return false
}

// LengthPrefixed scans an MP4-style access unit whose NAL units are prefixed
// with lengthSizeMinusOne+1 bytes. Used for avc1/hvc1 samples.
func LengthPrefixed(data []byte, lengthSize int, hevc bool) (bool, error) {
	if lengthSize < 1 || lengthSize > 4 {
		return false, fault.New(fault.KindFormat, "", -1,
			fmt.Sprintf("unsupported NAL length size %d", lengthSize)).Recover(false)
	}
	pos := 0
	for pos+lengthSize <= len(data) {
		n := 0
		for i := 0; i < lengthSize; i++ {
			n = n<<8 | int(data[pos+i])
		}
		pos += lengthSize
		if n <= 0 || pos+n > len(data) {
			break
		}
		h := data[pos]
		pos += n
		if hevc {
			t := (h >> 1) & 0x3F
			if t >= 16 && t <= 21 {
				return true, nil
			}
		} else {
			if h&0x1F == 5 {
				return true, nil
			}
		}
	}
	return false, nil
}
