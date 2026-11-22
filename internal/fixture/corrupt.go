package fixture

import (
	"bytes"
	"encoding/binary"
)

// Layout records structural offsets of one generated document, measured
// before any corruption is applied. Tests use it to assert byte-exact errors.
type Layout struct {
	SegmentDataOff int64
	ClusterOff     []int64
	// FirstBlockPayloadOff is the first payload byte of the first block of
	// the second cluster (used for precise truncation).
	FirstBlockPayloadOff int64
	BlockTrackByteOff    []int64 // track VINT byte of every simple block
	ClusterTimecodeOff   []int64 // first value byte of each cluster timecode
}

// layout is populated while a document is generated.
type layoutSink struct {
	lay Layout
}

// truncationOffset returns a byte offset inside the second cluster's first
// block payload, which is guaranteed to be a declared-but-unfulfilled byte.
func truncationOffset(doc []byte) int64 {
	if len(doc) == 0 {
		return -1
	}
	return int64(len(doc) - 1)
}

// applyCorruption mutates a generated document per the requested class.
// lay carries generation-time offsets so no ID scanning is needed.
func applyCorruption(doc []byte, opt Options, lay Layout) []byte {
	switch opt.Corrupt {
	case CorruptClusterTruncate:
		if lay.FirstBlockPayloadOff > 0 && int(lay.FirstBlockPayloadOff) < len(doc) {
			// Keep the block header and 2 payload bytes; the remaining declared
			// payload is missing, so the first illegal byte equals the new EOF.
			cut := int(lay.FirstBlockPayloadOff) + 2
			if cut < len(doc) {
				return doc[:cut]
			}
		}
		return doc[:len(doc)-1]
	case CorruptMissingTrack:
		return retargetBlocks(doc, 9, lay)
	case CorruptVFRMisalign:
		return shiftAlternateClusters(doc, lay)
	case CorruptWrapDrift:
		return injectHalfPeriodJump(doc, lay)
	default:
		return doc
	}
}

// retargetBlocks rewrites the track VINT byte of every simple block.
func retargetBlocks(doc []byte, newTrack uint64, lay Layout) []byte {
	out := append([]byte(nil), doc...)
	var b Buffer
	b.VInt(newTrack, 1)
	for _, pos := range lay.BlockTrackByteOff {
		if int(pos) < len(out) {
			out[pos] = b.Bytes()[0]
		}
	}
	return out
}

// shiftAlternateClusters adds 13 ms to every even-indexed cluster timecode.
func shiftAlternateClusters(doc []byte, lay Layout) []byte {
	out := append([]byte(nil), doc...)
	for i, p := range lay.ClusterTimecodeOff {
		if i%2 != 0 || int(p)+4 > len(out) {
			continue
		}
		v := binary.BigEndian.Uint32(out[p : p+4])
		v += 13
		binary.BigEndian.PutUint32(out[p:p+4], v)
	}
	return out
}

// injectHalfPeriodJump forces a raw jump of 2^31 ticks at the second cluster.
func injectHalfPeriodJump(doc []byte, lay Layout) []byte {
	out := append([]byte(nil), doc...)
	if len(lay.ClusterTimecodeOff) < 2 {
		return out
	}
	p := lay.ClusterTimecodeOff[1]
	if int(p)+4 > len(out) {
		return out
	}
	binary.BigEndian.PutUint32(out[p:p+4], uint32(1)<<31)
	return out
}

// safeIndex reports whether off is within b.
func safeIndex(b []byte, off int64) bool { return off >= 0 && int(off) < len(b) }

var _ = bytes.MinRead
