package rtp

import (
	"github.com/pion/rtp"

	"continuity/internal/media"
)

// Block is one fixed-size slice of the reconstructed sample timeline.
// GapSample ranges are explicit: PCM there is a deterministic silence
// placeholder and never a fabricated real sample.
type Block struct {
	Index       int
	StartSample int64
	EndSample   int64
	PCM         []byte
	GapSamples  int64
	GapRanges   []GapRange
}

// GapRange marks [Start,End) samples within the global timeline that
// could not be reconstructed from received RTP packets.
type GapRange struct {
	StartSample int64
	EndSample   int64
}

// Ingest receives raw RTP datagrams, orders them through a Window and
// rebuilds the PCM sample timeline in fixed-size blocks. All timestamps
// are interpreted through the shared sample clock; retained memory grows
// only with the block size and the reorder window width.
type Ingest struct {
	win           *Window
	clock         media.Clock
	clockRate     uint32
	channels      uint16
	bits          uint16
	blockFrames   int64
	samplesPerPkt int64
	frameBytes    int64
	lastTS        uint32
	haveTS        bool
	maxAlignSmp   int64
	received      int
	blocks        []Block
	// accumulator for the block currently being filled
	curPCM    []byte
	curBase   int64 // global sample index of curPCM[0]
	curFilled int64 // samples already placed in curPCM
	gapQueue  []Gap // finalized gaps not yet consumed into blocks
}

// Config parameterizes an Ingest.
type Config struct {
	ClockRate     uint32
	Channels      uint16
	BitsPerSample uint16
	WindowWidth   uint16
	BlockFrames   int64
}

// NewIngest builds an Ingest. All packets carry the same fixed payload
// size, which defines how many samples each sequence number covers.
func NewIngest(c media.Clock, cfg Config) *Ingest {
	return &Ingest{
		win:         NewWindow(cfg.WindowWidth),
		clock:       c,
		clockRate:   cfg.ClockRate,
		channels:    cfg.Channels,
		bits:        cfg.BitsPerSample,
		blockFrames: cfg.BlockFrames,
		frameBytes:  int64(cfg.Channels) * int64(cfg.BitsPerSample) / 8,
	}
}

// PushPacket parses one datagram and feeds it into the reorder window.
func (in *Ingest) PushPacket(raw []byte) (Outcome, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return OutDuplicate, media.WrapError(media.CatInvalidInput, "rtp: malformed packet", err)
	}
	if !in.haveTS {
		in.haveTS = true
		in.lastTS = pkt.Timestamp
		in.samplesPerPkt = int64(len(pkt.Payload)) / in.frameBytes
	} else {
		in.checkAlignment(pkt.Timestamp)
		in.lastTS = pkt.Timestamp
	}
	in.received++
	out, delivered := in.win.Push(pkt.SequenceNumber, append([]byte(nil), pkt.Payload...))
	in.syncGaps()
	if in.samplesPerPkt > 0 {
		first := in.win.FirstSeq()
		in.advanceToGaps(first)
		for _, d := range delivered {
			in.place(first, d)
		}
	}
	return out, nil
}

// syncGaps rebuilds the pending gap queue from the window's confirmed
// gaps. Retractions (late in-window packets filling a hole) shrink or
// remove entries automatically.
func (in *Ingest) syncGaps() {
	in.gapQueue = append(in.gapQueue[:0], in.win.FinalizedGaps()...)
}

// advanceToGaps flushes blocks whose whole sample range is already known
// to sit before the next still-missing packet, so gap-only blocks are
// emitted even when no later packet has arrived yet.
func (in *Ingest) advanceToGaps(firstSeq uint64) {
	if len(in.gapQueue) == 0 || in.curPCM == nil {
		return
	}
	gapStartSample := int64(in.gapQueue[0].StartSeq-firstSeq) * in.samplesPerPkt
	for in.curPCM != nil && in.curBase+in.blockFrames <= gapStartSample {
		in.flushBlock()
	}
}

// checkAlignment measures the error, in samples, between the RTP
// timestamp increment and the integer sample position it implies.
func (in *Ingest) checkAlignment(ts uint32) {
	// Use signed 32-bit delta so normal forward increments (including
	// values that cross the uint32 wrap) stay positive.
	delta := int64(int32(ts - in.lastTS))
	samples := in.clock.TimestampToSamples(delta, in.clockRate)
	back := in.clock.SamplesToTimestamp(samples, in.clockRate)
	tsErr := media.AbsInt64(int64(int32(back)) - delta)
	// Convert timestamp-unit error back into samples with the same rule.
	smpErr := in.clock.TimestampToSamples(tsErr, in.clockRate)
	if smpErr > in.maxAlignSmp {
		in.maxAlignSmp = smpErr
	}
}

// place writes one delivered payload at its global sample offset,
// creating and flushing blocks as the accumulator fills.
func (in *Ingest) place(firstSeq uint64, d Delivered) {
	globalStart := (int64(d.Seq - firstSeq)) * in.samplesPerPkt
	for {
		if in.curPCM == nil {
			in.startBlock(int64(len(in.blocks)) * in.blockFrames)
		}
		off := globalStart - in.curBase
		if off >= in.blockFrames {
			// The packet belongs to a later block; blocks are created in
			// delivery order, so flush and continue.
			in.flushBlock()
			continue
		}
		copy(in.curPCM[off*in.frameBytes:], d.Payload)
		if filled := off + in.samplesPerPkt; filled > in.curFilled {
			in.curFilled = filled
		}
		if in.curFilled >= in.blockFrames {
			in.flushBlock()
		}
		return
	}
}

func (in *Ingest) startBlock(base int64) {
	in.curPCM = make([]byte, in.blockFrames*in.frameBytes)
	in.curBase = base
	in.curFilled = 0
}

func (in *Ingest) flushBlock() {
	blockStart, blockEnd := in.curBase, in.curBase+in.blockFrames
	ranges := in.consumeGapRanges(blockStart, blockEnd)
	b := Block{
		Index:       len(in.blocks),
		StartSample: blockStart,
		EndSample:   blockEnd,
		PCM:         in.curPCM,
		GapRanges:   ranges,
	}
	for _, g := range ranges {
		b.GapSamples += g.EndSample - g.StartSample
	}
	in.blocks = append(in.blocks, b)
	in.curPCM = nil
	in.curBase += in.blockFrames
	in.curFilled = 0
}

// consumeGapRanges converts sequence gaps overlapping [start,end) into
// sample ranges clipped to the block, and drops fully consumed gaps.
func (in *Ingest) consumeGapRanges(start, end int64) []GapRange {
	var out []GapRange
	rest := in.gapQueue[:0]
	for _, g := range in.gapQueue {
		gs := int64(g.StartSeq-in.win.FirstSeq()) * in.samplesPerPkt
		ge := (int64(g.EndSeq-in.win.FirstSeq()) + 1) * in.samplesPerPkt
		s, e := gs, ge
		if s < start {
			s = start
		}
		if e > end {
			e = end
		}
		if s < e {
			out = append(out, GapRange{StartSample: s, EndSample: e})
		}
		if ge > end {
			rest = append(rest, g) // gap spans into a later block
		}
	}
	in.gapQueue = rest
	return out
}

// Flush ends the stream, finalizing tail gaps and emitting the last
// partial block (padded with explicit gap metadata).
func (in *Ingest) Flush() ([]Block, Stats, error) {
	delivered, gaps := in.win.Flush()
	// Replace the queue with the complete final gap list (all gaps,
	// including ones confirmed only at stream end).
	in.gapQueue = append(in.gapQueue[:0], gaps...)
	if in.samplesPerPkt > 0 {
		first := in.win.FirstSeq()
		for _, d := range delivered {
			in.place(first, d)
		}
	}
	if in.curPCM != nil && in.curFilled > 0 {
		blockStart := in.curBase
		ranges := in.consumeGapRanges(blockStart, blockStart+in.blockFrames)
		if in.curFilled < in.blockFrames {
			ranges = append(ranges, GapRange{
				StartSample: blockStart + in.curFilled,
				EndSample:   blockStart + in.blockFrames,
			})
		}
		b := Block{
			Index:       len(in.blocks),
			StartSample: blockStart,
			EndSample:   blockStart + in.blockFrames,
			PCM:         in.curPCM,
			GapRanges:   ranges,
		}
		for _, g := range ranges {
			b.GapSamples += g.EndSample - g.StartSample
		}
		in.blocks = append(in.blocks, b)
		in.curPCM = nil
	}
	return append([]Block(nil), in.blocks...), in.win.Stats(), nil
}

// MaxAlignmentErrorSamples returns the largest timestamp-to-sample
// alignment error observed, expressed in whole samples.
func (in *Ingest) MaxAlignmentErrorSamples() int64 { return in.maxAlignSmp }

// Received returns the number of valid datagrams accepted for parsing.
func (in *Ingest) Received() int { return in.received }
