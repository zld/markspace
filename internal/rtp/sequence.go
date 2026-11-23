package rtp

// SeqClass describes how a wire sequence number relates to the stream.
type SeqClass int

const (
	SeqInOrder SeqClass = iota
	SeqReorder             // arrived behind the current maximum
	SeqDuplicate           // extended number already emitted a sample
	SeqWrap                // legitimate 16-bit rollover
	SeqAmbiguous           // jump too large to assign a cycle safely
)

const (
	// maxReorder bounds how far behind the maximum a packet may lag
	// before its cycle assignment is considered unknowable.
	maxReorder = 512
	// maxForwardJump bounds a plausible forward leap (loss burst).
	maxForwardJump = 16384
	// seenWindow bounds the duplicate-detection history.
	seenWindow = 4096
)

// Extender maps 16-bit wire sequence numbers to a monotonic 64-bit
// domain, detecting reorder, duplicates and rollover. One instance per
// SSRC. All decisions are deterministic functions of the input order.
type Extender struct {
	init   bool
	maxExt uint64
	seen   map[uint64]struct{}
}

// Extend assigns an extended sequence number to seq.
func (e *Extender) Extend(seq uint16) (uint64, SeqClass) {
	if !e.init {
		e.init = true
		e.maxExt = uint64(seq)
		e.seen = map[uint64]struct{}{e.maxExt: {}}
		return e.maxExt, SeqInOrder
	}
	diff := int32(int16(seq - uint16(e.maxExt)))
	switch {
	case diff > 0:
		if diff > maxForwardJump {
			return 0, SeqAmbiguous
		}
		ext := e.maxExt + uint64(diff)
		// A forward move whose wire value decreased crossed 0xFFFF.
		class := SeqInOrder
		if seq < uint16(e.maxExt) {
			class = SeqWrap
		}
		e.maxExt = ext
		e.remember(ext)
		return ext, class
	case diff == 0:
		return e.maxExt, SeqDuplicate
	default: // diff < 0, behind the maximum
		back := -diff
		if back > maxReorder {
			// Could be a very old duplicate or a mis-keyed cycle:
			// refuse to guess.
			return 0, SeqAmbiguous
		}
		ext := e.maxExt - uint64(back)
		if _, ok := e.seen[ext]; ok {
			return ext, SeqDuplicate
		}
		e.remember(ext)
		return ext, SeqReorder
	}
}

func (e *Extender) remember(ext uint64) {
	e.seen[ext] = struct{}{}
	if floor := e.maxExt - seenWindow; ext < floor {
		delete(e.seen, ext)
	}
}
