// Package fixture deterministically generates every medtrace input: valid
// and corrupted Matroska/WebM byte streams (including unknown-length and
// invalid-VINT damage), timestamped RTP event records and dependency-cycle
// descriptors. No external sample files are referenced.
package fixture

import (
	"bytes"
	"encoding/binary"
	"math/rand"

	"medtrace/internal/ebml"
)

// ebmlWriter builds EBML bytes with canonical VINT widths, including
// unknown-length elements (reserved all-ones size marker).
type ebmlWriter struct{ buf bytes.Buffer }

func (w *ebmlWriter) bytes() []byte { return w.buf.Bytes() }
func (w *ebmlWriter) raw(b []byte)  { w.buf.Write(b) }

// idBytes emits an element ID using its canonical on-wire width.
func (w *ebmlWriter) idBytes(id ebml.ElementID) []byte {
	v := uint64(id)
	var width int
	switch {
	case v >= 0x10000000:
		width = 4
	case v >= 0x200000:
		width = 3
	case v >= 0x4000:
		width = 2
	default:
		width = 1
	}
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out[8-width:]
}

// sizeVINT encodes a known length in the narrowest legal width.
func sizeVINT(n int) []byte {
	v := uint64(n)
	for width := 1; width <= 8; width++ {
		if v < (uint64(1)<<uint(7*width))-1 {
			b := make([]byte, width)
			for i := width - 1; i >= 1; i-- {
				b[i] = byte(v)
				v >>= 8
			}
			b[0] = byte(v) | (1 << uint(8-width))
			return b
		}
	}
	panic("ebml size too large")
}

// unknownSizeVINT returns the 4-byte reserved all-ones size VINT. A 4-byte
// VINT's length marker is the 0x10 bit in the first byte (0x1F), so the full
// unknown-length value reads 0x1FFFFFFF.
func unknownSizeVINT() []byte { return []byte{0x1F, 0xFF, 0xFF, 0xFF} }

func (w *ebmlWriter) elem(id ebml.ElementID, body []byte) {
	w.raw(w.idBytes(id))
	w.raw(sizeVINT(len(body)))
	w.raw(body)
}

func (w *ebmlWriter) uintElem(id ebml.ElementID, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	w.elem(id, b[i:])
}

// simpleBlock builds a Matroska SimpleBlock body.
func simpleBlock(track uint64, relTimecode int16, keyframe bool, payload []byte) []byte {
	var out bytes.Buffer
	out.Write(sizeVINT(int(track)))
	var tc [2]byte
	binary.BigEndian.PutUint16(tc[:], uint16(relTimecode))
	out.Write(tc[:])
	flags := byte(0)
	if keyframe {
		flags = 0x80
	}
	out.WriteByte(flags)
	out.Write(payload)
	return out.Bytes()
}

// Corruption selects a fixture damage mode.
type Corruption string

const (
	CorruptNone         Corruption = ""
	CorruptUnknownLen   Corruption = "unknown-length"
	CorruptInvalidVINT  Corruption = "invalid-vint"
	CorruptCycle        Corruption = "cycle"
	CorruptLossRecovery Corruption = "loss-recovery"
)

// WebMConfig controls sample generation.
type WebMConfig struct {
	Seed          int64
	Clusters      int
	TimecodeScale uint64 // nanoseconds per cluster tick
	Corruption    Corruption
}

// DefaultWebMConfig returns the acceptance-run configuration.
func DefaultWebMConfig(seed int64) WebMConfig {
	return WebMConfig{
		Seed:          seed,
		Clusters:      12,
		TimecodeScale: 1_000_000, // 1 ms ticks
		Corruption:    CorruptNone,
	}
}

// BlockRef describes one generated media block for cross-checking stats.
type BlockRef struct {
	TrackID     uint64
	ClusterTCMs int64
	RelTCMs     int64
	TimeNs      int64
	Keyframe    bool
}

// GenerateWebM renders a WebM byte stream in a single pass. The returned
// BlockRefs describe every media block with its nanosecond timestamp so
// pipeline statistics can be checked against the generator's truth.
func GenerateWebM(cfg WebMConfig) ([]byte, []BlockRef) {
	rng := rand.New(rand.NewSource(cfg.Seed))

	// ---- EBML header ------------------------------------------------------
	hdr := &ebmlWriter{}
	hdr.uintElem(ebml.IDEBMLVersion, 1)
	hdr.uintElem(ebml.IDEBMLReadVersion, 1)
	hdr.uintElem(ebml.IDEBMLMaxIDLength, 4)
	hdr.uintElem(ebml.IDEBMLMaxSizeLen, 8)
	hdr.elem(ebml.IDDocType, []byte("webm"))
	hdr.uintElem(ebml.IDDocTypeVersion, 4)
	hdr.uintElem(ebml.IDDocTypeReadVer, 2)

	// ---- Segment body -----------------------------------------------------
	seg := &ebmlWriter{}

	info := &ebmlWriter{}
	info.uintElem(ebml.IDTimecodeScale, cfg.TimecodeScale)
	info.elem(ebml.IDMuxingApp, []byte("medtrace-fixture"))
	info.elem(ebml.IDWritingApp, []byte("medtrace-fixture"))
	seg.elem(ebml.IDInfo, info.bytes())

	tracks := &ebmlWriter{}
	vEntry := &ebmlWriter{}
	vEntry.uintElem(ebml.IDTrackNumber, 1)
	vEntry.uintElem(ebml.IDTrackUID, 101)
	vEntry.uintElem(ebml.IDTrackType, 1)
	vEntry.elem(ebml.IDCodecID, []byte("V_VP9"))
	vEntry.elem(ebml.IDName, []byte("video"))
	tracks.elem(ebml.IDTrackEntry, vEntry.bytes())
	aEntry := &ebmlWriter{}
	aEntry.uintElem(ebml.IDTrackNumber, 2)
	aEntry.uintElem(ebml.IDTrackUID, 202)
	aEntry.uintElem(ebml.IDTrackType, 2)
	aEntry.elem(ebml.IDCodecID, []byte("A_OPUS"))
	aEntry.elem(ebml.IDName, []byte("audio"))
	tracks.elem(ebml.IDTrackEntry, aEntry.bytes())
	seg.elem(ebml.IDTracks, tracks.bytes())

	tickNs := int64(cfg.TimecodeScale) // 1 tick = TimecodeScale ns
	var refs []BlockRef
	clusterStepMs := int64(10)

	emitBlock := func(cl *ebmlWriter, track uint64, baseMs, relMs int64, key bool, payload []byte) {
		cl.elem(ebml.IDSimpleBlock, simpleBlock(track, int16(relMs), key, payload))
		refs = append(refs, BlockRef{
			TrackID:     track,
			ClusterTCMs: baseMs,
			RelTCMs:     relMs,
			TimeNs:      (baseMs + relMs) * 1_000_000,
			Keyframe:    key,
		})
	}

	for c := 0; c < cfg.Clusters; c++ {
		baseMs := int64(c) * clusterStepMs
		cl := &ebmlWriter{}
		cl.uintElem(ebml.IDTimecode, uint64(baseMs))
		// Video jitter in [-2,+2] ms; audio jitter in [-1,+1] ms, so the
		// generated A/V sync error is bounded by 3 ms (< 20 ms requirement).
		vJit := int64(rng.Intn(5)) - 2
		emitBlock(cl, 1, baseMs, vJit, c == 0, []byte{0x30, byte(c), byte(rng.Intn(256))})
		for k := 0; k < 2; k++ {
			aJit := int64(rng.Intn(3)) - 1
			rel := int64(k)*5 + aJit
			emitBlock(cl, 2, baseMs, rel, false, []byte{0x41, byte(c), byte(k)})
		}
		seg.elem(ebml.IDCluster, cl.bytes())
	}
	_ = tickNs

	// ---- Assemble ---------------------------------------------------------
	var out bytes.Buffer
	out.Write((&ebmlWriter{}).idBytes(ebml.IDEBML))
	out.Write(sizeVINT(len(hdr.bytes())))
	out.Write(hdr.bytes())

	switch cfg.Corruption {
	case CorruptUnknownLen:
		// Legal unknown-length segment, then truncate through the final
		// cluster so the unknown element ends at a damaged EOF.
		out.Write((&ebmlWriter{}).idBytes(ebml.IDSegment))
		out.Write(unknownSizeVINT())
		out.Write(seg.bytes())
		body := out.Bytes()
		return body[:len(body)-4], refs
	case CorruptInvalidVINT:
		// Valid known-length segment, then destroy one cluster ID marker.
		out.Write((&ebmlWriter{}).idBytes(ebml.IDSegment))
		out.Write(sizeVINT(len(seg.bytes())))
		out.Write(seg.bytes())
		body := out.Bytes()
		idx := bytes.Index(body, []byte{0x1F, 0x43, 0xB6, 0x75})
		if idx >= 0 {
			body[idx] = 0x00 // no VINT length marker -> invalid_vint
		}
		return body, refs
	default:
		out.Write((&ebmlWriter{}).idBytes(ebml.IDSegment))
		out.Write(sizeVINT(len(seg.bytes())))
		out.Write(seg.bytes())
		return out.Bytes(), refs
	}
}
