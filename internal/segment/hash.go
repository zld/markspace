// Package segment cuts the repaired stream into fixed-size packet segments,
// hashes each with SHA-256 and emits a summary that another node can
// independently recompute from the same bytes.
package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"

	"github.com/example/tsrepair/internal/index"
)

// Summary is the independently re-checkable record of one segment.
type Summary struct {
	Seq          int
	StartPacket  int64
	EndPacket    int64 // exclusive
	StartOffset  int64
	EndOffset    int64 // exclusive
	StartPTS     uint64
	EndPTS       uint64
	IndexRange   [2]int // keyframe IDs [first, last) inside the segment
	PacketCount  int
	Ordinals     []int64 // stable packet ordinals actually hashed, in order
	SHA256       string
}

// Hasher accumulates fixed-size segments over a run of TS packets.
type Hasher struct {
	packetsPerSegment int
	segments          []Summary
	currentPackets    int
	currentHash       hash.Hash
	startPacket       int64
	startOffset       int64
	startPTS          uint64
	lastPTS           uint64
	firstKeyframe     int
	lastKeyframe      int
	seq               int
	ordinals          []int64
}

// NewHasher creates a Hasher. packetsPerSegment must be >= 1.
func NewHasher(packetsPerSegment int) *Hasher {
	if packetsPerSegment < 1 {
		packetsPerSegment = 1
	}
	return &Hasher{
		packetsPerSegment: packetsPerSegment,
		currentHash:       sha256.New(),
		firstKeyframe:     -1,
	}
}

// AddPacket feeds one repaired packet (its raw 188 bytes) and the ordinal,
// byte offset and current PTS of that packet. The ordinal must be the
// packet's stable position in the byte stream so segment ranges stay valid
// after duplicates or recovered drops.
func (h *Hasher) AddPacket(ordinal, offset int64, pts uint64, raw []byte) {
	if h.currentPackets == 0 {
		h.startPacket, h.startOffset, h.startPTS = ordinal, offset, pts
	}
	h.currentHash.Write(raw)
	h.currentPackets++
	h.ordinals = append(h.ordinals, ordinal)
	h.lastPTS = pts
	if h.currentPackets == h.packetsPerSegment {
		h.finish(ordinal+1, offset+int64(len(raw)))
	}
}

// NoteKeyframe records that keyframe id falls inside the current segment.
func (h *Hasher) NoteKeyframe(id int) {
	if h.firstKeyframe < 0 {
		h.firstKeyframe = id
	}
	h.lastKeyframe = id + 1
}

// Flush closes a partially filled final segment (e.g. before a truncated
// tail). It returns false when nothing is buffered.
func (h *Hasher) Flush(endPacket, endOffset int64) bool {
	if h.currentPackets == 0 {
		return false
	}
	h.finish(endPacket, endOffset)
	return true
}

func (h *Hasher) finish(endPacket, endOffset int64) {
	sum := h.currentHash.Sum(nil)
	idxRange := [2]int{0, 0}
	if h.firstKeyframe >= 0 {
		idxRange = [2]int{h.firstKeyframe, h.lastKeyframe}
	}
	h.segments = append(h.segments, Summary{
		Seq:         h.seq,
		StartPacket: h.startPacket,
		EndPacket:   endPacket,
		StartOffset: h.startOffset,
		EndOffset:   endOffset,
		StartPTS:    h.startPTS,
		EndPTS:      h.lastPTS,
		IndexRange:  idxRange,
		PacketCount: h.currentPackets,
		Ordinals:    append([]int64(nil), h.ordinals...),
		SHA256:      hex.EncodeToString(sum),
	})
	h.seq++
	h.currentPackets = 0
	h.currentHash.Reset()
	h.ordinals = nil
	h.firstKeyframe, h.lastKeyframe = -1, 0
}

// Summaries returns a copy of every completed segment summary.
func (h *Hasher) Summaries() []Summary {
	return append([]Summary(nil), h.segments...)
}

// Recompute hashes the given raw segment data and compares it to the stored
// digest, returning whether an independent recomputation matches.
func (s Summary) Recompute(raw []byte) bool {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == s.SHA256
}

// Digest is a standalone helper for tests and callers that hash a buffer.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Report renders the summary lines used by the demo and audit logs.
func (s Summary) Report() string {
	return fmt.Sprintf(
		"segment=%d packets=[%d,%d) bytes=[%d,%d) pts=[%d,%d] index=%v packets=%d sha256=%s",
		s.Seq, s.StartPacket, s.EndPacket, s.StartOffset, s.EndOffset,
		s.StartPTS, s.EndPTS, s.IndexRange, s.PacketCount, s.SHA256)
}

// AttachIndexRanges fills IndexRange on completed summaries from an index by
// mapping each frame's PacketOffset into the segment that contains it.
func AttachIndexRanges(summaries []Summary, ix *index.Index) {
	if ix == nil {
		return
	}
	for i := range summaries {
		first, last := -1, -1
		for _, f := range ix.Frames {
			if f.PacketOffset >= summaries[i].StartPacket &&
				f.PacketOffset < summaries[i].EndPacket {
				if first < 0 {
					first = f.ID
				}
				last = f.ID + 1
			}
		}
		if first >= 0 {
			summaries[i].IndexRange = [2]int{first, last}
		}
	}
}
