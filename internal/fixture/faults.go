package fixture

import (
	"mediacore/ingest"
	"mediacore/media"
)

// PacketFaults describes packet-level damage applied before ingest.
type PacketFaults struct {
	// DuplicateEvery duplicates every Nth packet (0 disables).
	DuplicateEvery int
	// SwapEvery swaps adjacent packets every Nth (0 disables): reordering.
	SwapEvery int
	// BurstLossStart and BurstLossCount remove a contiguous run.
	BurstLossStart int
	BurstLossCount int
	// LateCount moves the first LateCount packets to the very end, far beyond
	// the reorder window, so they are classified late.
	LateCount int
}

// ApplyPacketFaults encodes one packet per frame, then damages the sequence.
func ApplyPacketFaults(frames []*media.Frame, payloadSize int, f PacketFaults) ([]byte, error) {
	chain, err := encodePackets(frames, payloadSize)
	if err != nil {
		return nil, err
	}
	if f.BurstLossStart >= 0 && f.BurstLossCount > 0 {
		start := f.BurstLossStart
		end := start + f.BurstLossCount
		if start > len(chain) {
			start = len(chain)
		}
		if end > len(chain) {
			end = len(chain)
		}
		chain = append(append([]*ingest.Packet{}, chain[:start]...), chain[end:]...)
	}
	if f.DuplicateEvery > 0 {
		var out []*ingest.Packet
		for i, p := range chain {
			out = append(out, p)
			if i%f.DuplicateEvery == f.DuplicateEvery-1 {
				out = append(out, p)
			}
		}
		chain = out
	}
	if f.SwapEvery > 0 {
		// Swap adjacent packets periodically. Swapping far-apart indexes would
		// push packets outside the reorder window and turn intended reordering
		// into loss.
		for i := 0; i+1 < len(chain); i += f.SwapEvery {
			chain[i], chain[i+1] = chain[i+1], chain[i]
		}
	}
	if f.LateCount > 0 && len(chain) > 0 {
		// Remove a mid-stream run and append it after everything else. The
		// later packets run past the reorder window, the hole is declared, and
		// the delayed run then arrives after its gap closed.
		n := f.LateCount
		if n > len(chain)/4 {
			n = len(chain) / 4
		}
		start := len(chain) / 4
		if start+n > len(chain) {
			start = len(chain) - n
		}
		late := append([]*ingest.Packet{}, chain[start:start+n]...)
		rest := append([]*ingest.Packet{}, chain[:start]...)
		rest = append(rest, chain[start+n:]...)
		chain = append(rest, late...)
	}
	var out []byte
	for _, p := range chain {
		b, err := ingest.EncodePacket(p)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

func encodePackets(frames []*media.Frame, payloadSize int) ([]*ingest.Packet, error) {
	if payloadSize <= 0 {
		payloadSize = PacketPayload
	}
	chain := make([]*ingest.Packet, len(frames))
	for i, f := range frames {
		p := &ingest.Packet{
			StreamID:  f.StreamID,
			Seq:       uint16(f.Seq),
			Timestamp: f.RTPTimestamp,
			Marker:    true,
			SSRC:      0x11223344,
			Payload:   make([]byte, payloadSize),
		}
		for j := range p.Payload {
			p.Payload[j] = byte(f.Seq + uint32(j))
		}
		chain[i] = p
	}
	return chain, nil
}

// Damage describes frame-stream byte damage.
type Damage struct {
	// ChecksumFrame flips a payload byte of this 1-based frame (digest
	// mismatch). Zero disables it.
	ChecksumFrame int
	// TruncateTail removes this many bytes from the end of the stream. Zero
	// disables it.
	TruncateTail int
	// ChangeFrameContent rewrites a pixel byte without fixing the digest for
	// this 1-based frame. Zero disables it.
	ChangeFrameContent int
}

// ApplyFrameDamage returns a damaged copy of a frame stream. Byte offsets are
// computed from the plan's exact record size.
func ApplyFrameDamage(data []byte, recordSize int, d Damage) ([]byte, int64) {
	out := append([]byte(nil), data...)
	var damagedOffset int64 = -1
	// Frame indexes are 1-based sentinels in this API so the zero value of
	// Damage means "leave the stream untouched".
	if d.ChecksumFrame > 0 && d.ChecksumFrame*recordSize <= len(out) {
		off := (d.ChecksumFrame - 1) * recordSize
		// Flip a payload byte (header is 96 bytes) so only the hash breaks.
		out[off+media.FrameHeaderSize+5] ^= 0xFF
		damagedOffset = int64(off)
	}
	if d.ChangeFrameContent > 0 && d.ChangeFrameContent*recordSize <= len(out) {
		off := (d.ChangeFrameContent - 1) * recordSize
		out[off+media.FrameHeaderSize+2] ^= 0x5A
		damagedOffset = int64(off)
	}
	if d.TruncateTail > 0 {
		keep := len(out) - d.TruncateTail
		if keep < 0 {
			keep = 0
		}
		damagedOffset = int64(keep)
		out = out[:keep]
	}
	return out, damagedOffset
}

// Repair describes how to reconstruct a clean stream after damage by
// regenerating the affected frames, which is how a user "fixes the input".
type Repair struct{}

// RebuildPlanWithFrames regenerates frames with the plan and returns an
// encoded stream; used by the demo to show that a repaired input verifies.
func RebuildStream(plan Plan, opts Options) ([]byte, []*media.Frame, error) {
	frames, err := plan.BuildFrames(opts)
	if err != nil {
		return nil, nil, err
	}
	data, err := EncodeFrames(frames)
	if err != nil {
		return nil, nil, err
	}
	return data, frames, nil
}

// RecordSizeFor is the encoded size of a clean frame for the given options.
func RecordSizeFor(opts Options) int {
	o := opts.normalized()
	return media.FrameHeaderSize + int(o.Width)*int(o.Height) + MetaSize
}
