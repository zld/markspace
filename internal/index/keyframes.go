// Package index builds the keyframe index. Each entry records the exact TS
// packet byte offset of an IDR/keyframe so anomalies (for example a continuity
// counter jump) can be associated with the keyframe interval that contains
// them, and segment cuts can be located back to packet offsets.
package index

import (
	"bytes"

	"mediapack/internal/ts"
)

// IDR NAL start-code patterns (4-byte and 3-byte) followed by nal_type=5.
var (
	idr4 = []byte{0x00, 0x00, 0x00, 0x01, 0x65}
	idr3 = []byte{0x00, 0x00, 0x01, 0x65}
)

// Keyframe is one indexed random-access point.
type Keyframe struct {
	Offset      int64  `json:"offset"`
	PacketIndex int64  `json:"packet_index"`
	PID         uint16 `json:"pid"`
}

// IsIDR reports whether the packet payload contains an H.264 IDR NAL.
func IsIDR(p *ts.Packet) bool {
	if p == nil || len(p.Payload) == 0 {
		return false
	}
	return bytes.Contains(p.Payload, idr4) || bytes.Contains(p.Payload, idr3)
}

// KeyframeIndex is an ordered, offset-addressable set of keyframes.
type KeyframeIndex struct {
	Keys []Keyframe `json:"keyframes"`
}

// New creates an empty index.
func New() *KeyframeIndex { return &KeyframeIndex{} }

// AddPacket indexes p if it is a keyframe. Returns true when added.
func (idx *KeyframeIndex) AddPacket(p *ts.Packet) bool {
	if !IsIDR(p) {
		return false
	}
	// De-duplicate repeated observations at the same offset.
	if n := len(idx.Keys); n > 0 && idx.Keys[n-1].Offset == p.Offset {
		return false
	}
	idx.Keys = append(idx.Keys, Keyframe{Offset: p.Offset, PacketIndex: p.Index, PID: p.PID})
	return true
}

// Exact returns the keyframe recorded at exactly offset, or nil.
func (idx *KeyframeIndex) Exact(offset int64) *Keyframe {
	for i := range idx.Keys {
		if idx.Keys[i].Offset == offset {
			return &idx.Keys[i]
		}
	}
	return nil
}

// AtOrBefore returns the greatest keyframe with offset <= target.
func (idx *KeyframeIndex) AtOrBefore(target int64) *Keyframe {
	var hit *Keyframe
	for i := range idx.Keys {
		if idx.Keys[i].Offset <= target {
			hit = &idx.Keys[i]
		} else {
			break
		}
	}
	return hit
}

// IntervalFor maps an input offset to its keyframe-bounded interval
// [keyframe.Offset, nextKeyframeOffset) capped at fileEnd.
func (idx *KeyframeIndex) IntervalFor(offset, fileEnd int64) (kf *Keyframe, start, end int64) {
	kf = idx.AtOrBefore(offset)
	if kf == nil {
		return nil, 0, fileEnd
	}
	start = kf.Offset
	end = fileEnd
	for i := range idx.Keys {
		if idx.Keys[i].Offset > kf.Offset {
			end = idx.Keys[i].Offset
			break
		}
	}
	return kf, start, end
}

// HitRate returns hits/len(probes) where a probe hits when a keyframe exists
// at exactly the probed offset. An empty probe set yields 1.0.
func (idx *KeyframeIndex) HitRate(probes []int64) float64 {
	if len(probes) == 0 {
		return 1.0
	}
	hits := 0
	for _, off := range probes {
		if idx.Exact(off) != nil {
			hits++
		}
	}
	return float64(hits) / float64(len(probes))
}
