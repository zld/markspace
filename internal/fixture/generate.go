// Package fixture generates deterministic Matroska/WebM test captures and
// deliberately corrupt variants. It is the only source of input bytes; the
// tool never downloads or re-encodes media.
package fixture

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/rand"

	"mediad/internal/ebml"
)

// TrackSpec describes one synthetic track.
type TrackSpec struct {
	Number     uint64
	Type       ebml.TrackType
	CodecID    string
	FrameNanos int64 // nominal frame duration
	KeyEvery   int   // keyframe interval in frames (video only)
	AudioRate  int   // audio blocks per second (audio only)
}

// Spec controls document generation.
type Spec struct {
	Seed            int64
	DurationSec     int
	VideoTracks     int
	AudioTracks     int
	Representations int
	UnknownSegment  bool
	UnknownClusters int // number of leading clusters written with unknown size
}

// Result is a generated document plus ground truth.
type Result struct {
	Data           []byte
	DocType        string
	Tracks         []TrackSpec
	VideoKeyframes []int64 // nanoseconds
	DurationNS     int64
}

// Build produces a deterministic document for the spec.
func Build(spec Spec) (*Result, error) {
	if spec.DurationSec <= 0 {
		return nil, errors.New("fixture: duration must be positive")
	}
	if spec.VideoTracks < 1 {
		spec.VideoTracks = 1
	}
	if spec.AudioTracks < 1 {
		spec.AudioTracks = 1
	}
	if spec.Representations < 1 {
		spec.Representations = 1
	}
	rng := rand.New(rand.NewSource(spec.Seed))
	scale := uint64(1_000_000)
	frameNanos := int64(33_333_333)

	var tracks []TrackSpec
	next := uint64(1)
	for i := 0; i < spec.VideoTracks; i++ {
		tracks = append(tracks, TrackSpec{Number: next, Type: ebml.TrackVideo, CodecID: "V_VP9", FrameNanos: frameNanos, KeyEvery: 60})
		next++
	}
	for i := 0; i < spec.AudioTracks; i++ {
		tracks = append(tracks, TrackSpec{Number: next, Type: ebml.TrackAudio, CodecID: "A_OPUS", AudioRate: 1000})
		next++
	}

	var b bytes.Buffer
	writeEl(&b, ebml.IDEBML, headerPayload("matroska"))
	var seg bytes.Buffer
	writeEl(&seg, ebml.IDInfo, infoPayload(scale, float64(spec.DurationSec*1000)))
	writeEl(&seg, ebml.IDTracks, tracksPayload(tracks))

	durTicks := int64(spec.DurationSec * 1000)
	// Interleave frames into clusters of 1s.
	clusterTicks := int64(1000)
	var keyframes []int64
	for start := int64(0); start < durTicks; start += clusterTicks {
		end := start + clusterTicks
		if end > durTicks {
			end = durTicks
		}
		clusterKnown := int(start/clusterTicks) >= spec.UnknownClusters
		var clusterBody bytes.Buffer
		writeEl(&clusterBody, ebml.IDTimestamp, uints(uint64(start)))
		// One video frame per tick bucket, audio blocks on their own schedule.
		var blocks []blk
		frameTicks := int64(33)
		for tick := start; tick < end; tick += frameTicks {
			for _, t := range tracks {
				if t.Type != ebml.TrackVideo {
					continue
				}
				frameIdx := int((tick - start) / frameTicks)
				key := frameIdx%t.KeyEvery == 0
				blocks = append(blocks, blk{track: t.Number, tick: tick, key: key})
				if key {
					keyframes = append(keyframes, tick*int64(scale))
				}
			}
		}
		for tick := start; tick < end; tick += 20 {
			for _, t := range tracks {
				if t.Type != ebml.TrackAudio {
					continue
				}
				blocks = append(blocks, blk{track: t.Number, tick: tick, audio: true})
			}
		}
		stableSort(blocks)
		for _, x := range blocks {
			payload := blockPayload(x.track, int16(x.tick-start), x.key, rng, x.audio)
			writeEl(&clusterBody, ebml.IDSimpleBlock, payload)
		}
		if clusterKnown {
			writeEl(&seg, ebml.IDCluster, clusterBody.Bytes())
		} else {
			writeElUnknown(&seg, ebml.IDCluster, clusterBody.Bytes())
		}
	}
	if spec.UnknownSegment {
		writeElUnknown(&b, ebml.IDSegment, seg.Bytes())
	} else {
		writeEl(&b, ebml.IDSegment, seg.Bytes())
	}
	return &Result{
		Data: b.Bytes(), DocType: "matroska", Tracks: tracks,
		VideoKeyframes: keyframes, DurationNS: durTicks * int64(scale),
	}, nil
}

// blk is a block scheduled within a cluster.
type blk struct {
	track uint64
	tick  int64
	key   bool
	audio bool
}

// stableSort orders blocks by cluster-relative tick, keeping document order
// for equal ticks so interleaving is deterministic.
func stableSort(blocks []blk) {
	for i := 1; i < len(blocks); i++ {
		for j := i; j > 0 && blocks[j].tick < blocks[j-1].tick; j-- {
			blocks[j], blocks[j-1] = blocks[j-1], blocks[j]
		}
	}
}

func blockPayload(track uint64, rel int16, key bool, rng *rand.Rand, audio bool) []byte {
	var out []byte
	out = append(out, 0x80|byte(track))
	out = append(out, byte(uint16(rel)>>8), byte(uint16(rel)))
	var flags byte
	if key {
		flags = 0x80
	}
	if audio {
		flags = 0
	}
	out = append(out, flags)
	n := 40 + rng.Intn(40)
	body := make([]byte, n)
	rng.Read(body)
	if key {
		// VP9 keyframe magic so framing tools accept the payload.
		copy(body, []byte{0x82, 0x49, 0x83, 0x42})
	}
	return append(out, body...)
}

func headerPayload(docType string) []byte {
	var b bytes.Buffer
	writeEl(&b, ebml.IDDocType, []byte(docType))
	writeEl(&b, ebml.IDDocTypeVersion, uints(4))
	writeEl(&b, 0x4286, uints(1))         // DocTypeReadVersion
	writeEl(&b, 0x428F, []byte("mediad")) // DocTypeExtensionName
	return b.Bytes()
}

func infoPayload(scale uint64, durationMs float64) []byte {
	var b bytes.Buffer
	writeEl(&b, ebml.IDTimecodeScale, uints(scale))
	var dur [8]byte
	binary.BigEndian.PutUint64(dur[:], math.Float64bits(durationMs))
	writeEl(&b, ebml.IDDuration, dur[:])
	writeEl(&b, 0x7BA9, []byte("mediad")) // Title
	return b.Bytes()
}

func tracksPayload(tracks []TrackSpec) []byte {
	var b bytes.Buffer
	for _, t := range tracks {
		var e bytes.Buffer
		writeEl(&e, ebml.IDTrackNumber, uints(t.Number))
		writeEl(&e, ebml.IDTrackUID, uints(t.Number*7919))
		writeEl(&e, ebml.IDTrackType, uints(uint64(t.Type)))
		writeEl(&e, ebml.IDCodecID, []byte(t.CodecID))
		if t.FrameNanos > 0 {
			writeEl(&e, ebml.IDDefaultDuration, uints(uint64(t.FrameNanos)))
		}
		switch t.Type {
		case ebml.TrackVideo:
			var v bytes.Buffer
			writeEl(&v, ebml.IDPixelWidth, uints(640))
			writeEl(&v, ebml.IDPixelHeight, uints(360))
			writeEl(&e, ebml.IDVideo, v.Bytes())
		case ebml.TrackAudio:
			var a bytes.Buffer
			var f [8]byte
			binary.BigEndian.PutUint64(f[:], math.Float64bits(48000))
			writeEl(&a, ebml.IDSamplingFreq, f[:])
			writeEl(&a, ebml.IDChannels, uints(2))
			writeEl(&e, ebml.IDAudio, a.Bytes())
		}
		writeEl(&b, ebml.IDTrackEntry, e.Bytes())
	}
	return b.Bytes()
}

func uints(v uint64) []byte {
	var out []byte
	for v > 0 {
		out = append([]byte{byte(v)}, out...)
		v >>= 8
	}
	if len(out) == 0 {
		out = []byte{0}
	}
	return out
}

func idBytes(id uint32) []byte {
	switch {
	case id > 0xFFFFFF:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFF:
		return []byte{byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id)}
	}
}

func writeEl(b *bytes.Buffer, id uint32, payload []byte) {
	b.Write(idBytes(id))
	b.Write(ebml.EncodeSize(uint64(len(payload)), false))
	b.Write(payload)
}

func writeElUnknown(b *bytes.Buffer, id uint32, payload []byte) {
	b.Write(idBytes(id))
	b.Write(ebml.EncodeSizeWidth(0, 8, true))
	b.Write(payload)
}
