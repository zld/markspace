package pipeline

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"mediacheck/internal/codec"
	"mediacheck/internal/index"
	"mediacheck/internal/timeline"
)

// Fault selects an injected corruption for the self-generated demo data.
type Fault string

const (
	FaultNone          Fault = "none"
	FaultTruncated     Fault = "truncated"
	FaultInvalidLength Fault = "invalid-length"
)

// DemoSpec configures one generated elementary stream plus its index.
type DemoSpec struct {
	Track   string
	Codec   codec.Codec
	Framing codec.Framing
	Frames  int // number of pictures (keyframes every keyInterval)
	KeyInterval int
	Fault   Fault
}

// GeneratedStream bundles a synthesized stream with the index that should
// mostly reconcile against it and the ground-truth keyframe offsets.
type GeneratedStream struct {
	Track        string
	Codec        codec.Codec
	Framing      codec.Framing
	raw          []byte
	Data         *bytes.Reader
	Index        []index.Entry
	Base         timeline.TimeBase
	KeyOffsets   []int64
	ExpectDamage int // minimum damage ranges the fault must produce
}

// ResetReader rewinds the stream to its first byte for another run. Because
// the generator owns the bytes, repeated runs hash identical content.
func (g *GeneratedStream) ResetReader() { g.Data = bytes.NewReader(g.raw) }

// DemoTimeBase is the canonical analysis grid (90kHz, integer ticks).
var DemoTimeBase = timeline.TimeBase{Num: 1, Den: 90000}

// Generate builds a deterministic elementary stream. H.264/H.265 use Annex B,
// AV1 uses length-prefixed OBUs. Keyframes are emitted every keyInterval
// frames; the returned index references the same frames with a small,
// tolerance-friendly tick jitter plus one deliberate false positive.
func Generate(spec DemoSpec) (*GeneratedStream, error) {
	if spec.Frames <= 0 {
		spec.Frames = 24
	}
	if spec.KeyInterval <= 0 {
		spec.KeyInterval = 6
	}
	var (
		raw        []byte
		keyOffsets []int64
		indexTS    []int64
	)
	frameStep := int64(3000) // 90kHz / 30fps

	for f := 0; f < spec.Frames; f++ {
		isKey := f%spec.KeyInterval == 0
		var unit []byte
		switch spec.Codec {
		case codec.H264:
			unit = h264Unit(isKey, f)
		case codec.H265:
			unit = h265Unit(isKey, f)
		case codec.AV1:
			unit = av1Unit(isKey, f)
		default:
			return nil, fmt.Errorf("unsupported demo codec %q", spec.Codec)
		}
		framed := frameUnit(spec.Framing, unit)
		if isKey {
			keyOffsets = append(keyOffsets, int64(len(raw))+payloadOffset(spec.Framing))
			indexTS = append(indexTS, int64(f)*frameStep)
		}
		raw = append(raw, framed...)
	}

	raw, expectDamage, err := injectFault(spec, raw)
	if err != nil {
		return nil, err
	}

	entries := buildIndex(spec, indexTS, keyOffsets)
	return &GeneratedStream{
		Track: spec.Track, Codec: spec.Codec, Framing: spec.Framing,
		raw: raw, Data: bytes.NewReader(raw),
		Index: entries, Base: DemoTimeBase,
		KeyOffsets: keyOffsets, ExpectDamage: expectDamage,
	}, nil
}

func payloadOffset(f codec.Framing) int64 {
	if f == codec.AnnexB {
		return 4 // start code length
	}
	return 4 // length-prefix length
}

func frameUnit(f codec.Framing, unit []byte) []byte {
	if f == codec.AnnexB {
		out := make([]byte, 0, 4+len(unit))
		out = append(out, 0, 0, 0, 1)
		return append(out, unit...)
	}
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(len(unit)))
	return append(out, unit...)
}

// h264Unit returns a minimal H.264 NAL: type 5 (IDR) for keyframes, else 1.
func h264Unit(key bool, frame int) []byte {
	hdr := byte(0x41) // non-IDR slice, nal_ref_idc=2
	if key {
		hdr = 0x65 // IDR slice
	}
	return []byte{hdr, byte(frame), 0xAA, 0xBB}
}

// h265Unit returns a minimal HEVC NAL: type 19 (IDR) or type 1 (TRAIL_R).
func h265Unit(key bool, frame int) []byte {
	var hdr byte
	if key {
		hdr = byte(19 << 1) // IDR_W_RADL
	} else {
		hdr = byte(1 << 1) // TRAIL_R
	}
	return []byte{hdr, 0x01, byte(frame), 0xCC}
}

// av1Unit returns a length-prefixed OBU with a frame header encoding key vs
// inter frame, followed by deterministic payload bytes.
func av1Unit(key bool, frame int) []byte {
	hdr := byte(codec.OBUFrame<<3) | codec.OBUHasSize // type 6, has_size
	var frameHdr byte
	if !key {
		frameHdr = 1 << 5 // show_existing=0, frame_type=1 (INTER)
	}
	// frame_type=0 (KEY_FRAME) leaves the top bits zero.
	return []byte{hdr, frameHdr, byte(frame), 0xDD, 0xEE}
}

// injectFault applies the requested corruption and reports how many damage
// ranges it is expected to yield.
func injectFault(spec DemoSpec, raw []byte) ([]byte, int, error) {
	switch spec.Fault {
	case FaultNone, "":
		return raw, 0, nil
	case FaultTruncated:
		// Truncate inside the final framing boundary so the parser sees a
		// genuine cut-off unit rather than a complete final NAL:
		//  - Annex B: leave a dangling start code (trailing zero), so EOF
		//    lands mid start-code and is reported as a truncated tail;
		//  - length-prefixed: cut the final payload short of its declared
		//    length, detected at flush as an over-declared tail.
		if spec.Framing == codec.AnnexB {
			pos := lastStartCode(raw)
			if pos < 0 {
				return raw[:len(raw)/2], 1, nil
			}
			// Keep the full final start code (which flushes the preceding
			// unit as valid) and append a single dangling zero byte so EOF
			// lands inside a would-be next start code: a 1-byte truncated
			// tail that never touches the earlier valid unit.
			out := append(append([]byte{}, raw[:pos+4]...), 0x00)
			return out, 1, nil
		}
		cut := len(raw) - 2
		if cut < 4 {
			cut = len(raw) / 2
		}
		return raw[:cut], 1, nil
	case FaultInvalidLength:
		if spec.Framing != codec.LengthPrefixed {
			return nil, 0, fmt.Errorf("invalid-length fault needs length-prefixed framing")
		}
		// Corrupt one interior length prefix to claim a huge payload; the
		// resync logic must recover the following valid unit and emit a gap.
		pos := len(raw) / 2
		// Align to a 4-byte prefix boundary by walking back.
		for pos > 0 && pos%4 != 0 {
			pos--
		}
		if pos+4 > len(raw) {
			return raw, 0, nil
		}
		out := make([]byte, len(raw))
		copy(out, raw)
		binary.BigEndian.PutUint32(out[pos:pos+4], uint32(codec.MaxUnitBytes)+64)
		return out, 1, nil
	default:
		return nil, 0, fmt.Errorf("unknown fault %q", spec.Fault)
	}
}

// buildIndex creates a mostly-correct index: every real keyframe gets an
// entry with harmless +/-1 tick jitter (inside tolerance), plus one false
// positive that points at no keyframe.
func buildIndex(spec DemoSpec, ts []int64, offsets []int64) []index.Entry {
	entries := make([]index.Entry, 0, len(ts)+1)
	for i, t := range ts {
		jitter := int64(1)
		if i%2 == 0 {
			jitter = -1
		}
		off := int64(-1)
		if i < len(offsets) {
			off = offsets[i]
		}
		entries = append(entries, index.Entry{
			Track: spec.Track, TS: t + jitter,
			Base: DemoTimeBase, ByteOffset: off,
		})
	}
	// One false positive far from any real keyframe.
	if len(ts) > 0 {
		entries = append(entries, index.Entry{
			Track: spec.Track, TS: ts[len(ts)-1] + 50000,
			Base: DemoTimeBase, ByteOffset: 1 << 20,
		})
	}
	return entries
}

// TrackInput converts generated data into a pipeline input.
func (g *GeneratedStream) TrackInput() TrackInput {
	return TrackInput{
		Track: g.Track, Codec: g.Codec, Framing: g.Framing,
		Reader: g.Data, Index: g.Index, IndexBase: g.Base,
	}
}

// lastStartCode returns the offset of the last 4-byte Annex B start code, or
// -1 when none is present.
func lastStartCode(raw []byte) int {
	for i := len(raw) - 4; i >= 0; i-- {
		if raw[i] == 0 && raw[i+1] == 0 && raw[i+2] == 0 && raw[i+3] == 1 {
			return i
		}
	}
	return -1
}
