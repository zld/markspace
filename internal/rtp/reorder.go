package rtp

import (
	"sort"

	"rtpregress/internal/media"
)

// Pipeline stage names. A late packet is reported against the stage whose
// committed output would have to be rewritten to include it.
const (
	StageParse    = "parse"
	StageAlign    = "align"
	StageGenerate = "generate"
	StageVerify   = "verify"
)

// CommitStages lists the pipeline stages in execution order. Each stage
// consumes the previous one's output, so a late packet affects the earliest
// stage that already consumed its sequence range and never the later ones.
var CommitStages = []string{StageParse, StageAlign, StageGenerate, StageVerify}

// Gap is a contiguous run of sequence numbers that never arrived.
type Gap struct {
	// Start and End are the first and last missing sequence numbers in the
	// monotonic (wrap-extended) domain.
	Start, End uint64
}

// Count is the number of missing packets in the gap.
func (g Gap) Count() int { return int(g.End-g.Start) + 1 }

// GapList is an ordered list of gaps.
type GapList []Gap

// Total returns the total number of missing packets.
func (g GapList) Total() int {
	n := 0
	for _, gap := range g {
		n += gap.Count()
	}
	return n
}

// LatePacket is a packet that arrived after its sequence slot had been
// committed. It is never applied; it only reports where it would have
// mattered.
type LatePacket struct {
	Packet
	// Sequence is the wrap-extended sequence number.
	Sequence uint64
	// FirstStage is the earliest committed stage affected by this packet.
	FirstStage string
	// Position is the 1-based index of the committed packet it would have
	// preceded, i.e. the insertion point in the committed output.
	Position int
	// Duplicate is true when the sequence number was already committed, in
	// which case the packet is a duplicate rather than a late gap filler.
	Duplicate bool
}

// ReorderResult is the deterministic view of a trace after windowed
// reordering.
type ReorderResult struct {
	// Packets are the committed packets in sequence order. Duplicates and
	// late packets never appear here.
	Packets []Packet
	// Sequences is the wrap-extended sequence number of each committed
	// packet, parallel to Packets.
	Sequences []uint64
	// Gaps are the holes that the window closed without ever seeing.
	Gaps GapList
	// Duplicates counts the discarded copies of already-seen packets.
	Duplicates int
	// Late lists packets that missed their commit window.
	Late []LatePacket
	// Highest is the highest extended sequence number observed.
	Highest uint64
	// Window is the reorder window used.
	Window uint16
}

// Lost reports whether any sequence number was recorded as missing.
func (r *ReorderResult) Lost() bool { return len(r.Gaps) > 0 }

// ReorderOptions controls windowed reordering.
type ReorderOptions struct {
	// Window is how far past the next expected packet a sequence number may
	// arrive before the buffer commits up to it. A window of zero commits
	// strictly in arrival order, so every out-of-order packet becomes late.
	Window uint16
	// Stage is the pipeline stage that is currently committed; a late
	// packet is reported as first affecting this stage.
	Stage string
}

// Reorder applies sequence-number windowing to the packets of a trace.
//
// Packets are consumed in arrival order, never sorted: sorting by sequence
// number would erase the lateness the regression is meant to detect. A
// sequence number commits once we have seen a packet at least Window ahead of
// it. Unfilled holes below the commit point become recorded loss and can no
// longer be changed by a later arrival.
func Reorder(t *Trace, opts ReorderOptions) (*ReorderResult, error) {
	if err := (media.Timebase{Track: t.Header.Track, Timescale: t.Header.Timescale}).Validate(); err != nil {
		return nil, media.At(err, media.Loc{Stage: StageParse})
	}
	if opts.Stage == "" {
		opts.Stage = StageParse
	}
	if !knownStage(opts.Stage) {
		return nil, media.Newf(media.KindInput, "unknown commit stage %q", opts.Stage)
	}

	res := &ReorderResult{Window: opts.Window}
	if len(t.Packets) == 0 {
		return res, nil
	}

	// Wrap-extend sequence numbers in arrival order. A 16-bit signed step
	// means "just after", so 65535 -> 0 is +1 and a jump of 40000 backwards
	// is -25536.
	first := uint64(t.Packets[0].SequenceNumber)
	ext := make([]uint64, len(t.Packets))
	ext[0] = first
	for i := 1; i < len(t.Packets); i++ {
		prev := ext[i-1]
		step := int64(int16(uint16(t.Packets[i].SequenceNumber) - uint16(prev&0xffff)))
		if step >= 0 {
			ext[i] = prev + uint64(step)
			continue
		}
		back := uint64(-step)
		if back > prev {
			return nil, media.At(media.Newf(media.KindInput,
				"sequence number %d precedes the trace start after wrap",
				t.Packets[i].SequenceNumber),
				media.Loc{Track: t.Header.Track, Sample: i + 1, Stage: StageParse,
					Offset: t.Packets[i].Offset, HasOffset: true})
		}
		ext[i] = prev - back
	}

	var (
		next      = first
		pending   = make(map[uint64]Packet, int(opts.Window)+1)
		committed = make(map[uint64]bool, len(t.Packets))
	)
	res.Highest = first

	appendPacket := func(seq uint64, p Packet) {
		res.Packets = append(res.Packets, p)
		res.Sequences = append(res.Sequences, seq)
		committed[seq] = true
	}

	closeTo := func(upTo uint64) {
		if upTo < next {
			return
		}
		for seq := next; seq <= upTo; seq++ {
			if p, ok := pending[seq]; ok {
				appendPacket(seq, p)
				delete(pending, seq)
				continue
			}
			if !committed[seq] {
				// Coalesce with the previous gap when contiguous.
				if n := len(res.Gaps); n > 0 && res.Gaps[n-1].End+1 == seq {
					res.Gaps[n-1].End = seq
				} else {
					res.Gaps = append(res.Gaps, Gap{Start: seq, End: seq})
				}
			}
		}
		next = upTo + 1
	}

	for i, p := range t.Packets {
		seq := ext[i]
		if seq < next {
			// The slot is already committed: either a duplicate copy or a
			// late packet for a hole that was recorded as loss.
			if committed[seq] {
				res.Duplicates++
				res.Late = append(res.Late, LatePacket{
					Packet: p, Sequence: seq, Duplicate: true,
					FirstStage: opts.Stage, Position: insertionPoint(res.Sequences, seq),
				})
				continue
			}
			res.Late = append(res.Late, LatePacket{
				Packet: p, Sequence: seq,
				FirstStage: opts.Stage, Position: insertionPoint(res.Sequences, seq),
			})
			continue
		}
		if _, dup := pending[seq]; dup {
			// A duplicate that arrives before the slot commits changes
			// nothing at all, so it is counted but not reported as late.
			res.Duplicates++
			continue
		}
		pending[seq] = p
		if seq > res.Highest {
			res.Highest = seq
		}
		if ahead := seq - next; ahead >= uint64(opts.Window) {
			closeTo(seq - uint64(opts.Window))
		}
	}

	// End of trace: every remaining hole is real loss.
	closeTo(res.Highest)
	return res, nil
}

// insertionPoint returns the 1-based position at which seq would be inserted
// into the ordered committed sequence list.
func insertionPoint(seqs []uint64, seq uint64) int {
	return sort.Search(len(seqs), func(i int) bool { return seqs[i] >= seq }) + 1
}

func knownStage(s string) bool {
	for _, st := range CommitStages {
		if s == st {
			return true
		}
	}
	return false
}
