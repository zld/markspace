package rtp

import "github.com/pion/rtp"

// MaxFrameBytes caps the per-frame reassembly buffer (4 MiB).
const MaxFrameBytes = 4 << 20

// H264Stats summarizes H.264 frame reassembly.
type H264Stats struct {
	Frames        int `json:"frames"`         // complete frames
	DroppedFrames int `json:"dropped_frames"` // incomplete frames discarded
	NALUnits      int `json:"nal_units"`      // NAL units accepted
	BadPayloads   int `json:"bad_payloads"`   // malformed payloads
	SSRCChanges   int `json:"ssrc_changes"`   // SSRC changes observed
	OversizeDrops int `json:"oversize_drops"` // frames dropped for exceeding MaxFrameBytes
}

// FrameReassembler rebuilds H.264 frames from ordered RTP packets.
// Frames are delimited by RTP timestamp and the marker bit. Incomplete
// frames (timeout, timestamp jump, missing FU-A fragments, oversize)
// are discarded and counted.
type FrameReassembler struct {
	maxBytes int

	ssrc    uint32
	ssrcSet bool

	hasCur   bool
	curTS    uint32
	curBytes int
	curDone  bool // marker bit seen for current timestamp

	skipping bool   // ignore the rest of a dropped frame's timestamp
	skipTS   uint32
	fuActive bool // FU-A fragmentation in progress

	stats H264Stats
}

// NewFrameReassembler creates a reassembler with the 4 MiB frame cap.
func NewFrameReassembler() *FrameReassembler {
	return &FrameReassembler{maxBytes: MaxFrameBytes}
}

// Stats returns a copy of the current statistics.
func (f *FrameReassembler) Stats() H264Stats {
	return f.stats
}

// dropCurrent discards the in-progress frame and counts it once.
func (f *FrameReassembler) dropCurrent() {
	if f.hasCur {
		f.stats.DroppedFrames++
		f.skipping = true
		f.skipTS = f.curTS
	}
	f.hasCur = false
	f.fuActive = false
}

// addBytes grows the current frame, enforcing the size cap.
func (f *FrameReassembler) addBytes(n int) {
	f.curBytes += n
	if f.curBytes > f.maxBytes {
		f.stats.OversizeDrops++
		f.dropCurrent()
	}
}

// Process feeds one in-order packet into the reassembler.
func (f *FrameReassembler) Process(pkt *rtp.Packet) {
	if f.ssrcSet && pkt.SSRC != f.ssrc {
		f.dropCurrent()
		f.stats.SSRCChanges++
	}
	f.ssrc, f.ssrcSet = pkt.SSRC, true

	if f.skipping {
		if pkt.Timestamp == f.skipTS {
			return
		}
		f.skipping = false
	}

	// frame boundary: a new timestamp closes the previous frame
	if f.hasCur && pkt.Timestamp != f.curTS {
		if f.curDone {
			f.stats.Frames++
		} else {
			f.stats.DroppedFrames++
		}
		f.hasCur = false
		f.fuActive = false
	}
	if !f.hasCur {
		f.hasCur = true
		f.curTS = pkt.Timestamp
		f.curBytes = 0
		f.curDone = false
		f.fuActive = false
	}

	p := pkt.Payload
	if len(p) == 0 {
		f.stats.BadPayloads++
		f.dropCurrent()
		return
	}

	switch nalType := p[0] & 0x1F; {
	case nalType >= 1 && nalType <= 23: // single NAL unit
		if f.fuActive {
			f.stats.BadPayloads++
			f.dropCurrent()
			return
		}
		f.stats.NALUnits++
		f.addBytes(len(p))
		if pkt.Marker {
			f.curDone = true
		}

	case nalType == 24: // STAP-A
		if f.fuActive {
			f.stats.BadPayloads++
			f.dropCurrent()
			return
		}
		off := 1
		for off < len(p) {
			if off+2 > len(p) {
				f.stats.BadPayloads++
				f.dropCurrent()
				return
			}
			size := int(p[off])<<8 | int(p[off+1])
			off += 2
			if size == 0 || off+size > len(p) {
				f.stats.BadPayloads++
				f.dropCurrent()
				return
			}
			f.stats.NALUnits++
			f.addBytes(size)
			off += size
		}
		if pkt.Marker {
			f.curDone = true
		}

	case nalType == 28: // FU-A
		if len(p) < 2 {
			f.stats.BadPayloads++
			f.dropCurrent()
			return
		}
		indicator, header := p[0], p[1]
		start := header&0x80 != 0
		end := header&0x40 != 0
		if start {
			if f.fuActive { // previous fragmentation never completed
				f.stats.BadPayloads++
				f.dropCurrent()
				return
			}
			f.fuActive = true
			f.stats.NALUnits++
			// reconstructed NAL header: (indicator & 0xE0) | (header & 0x1F)
			_ = indicator
			f.addBytes(1 + len(p) - 2)
			return
		}
		if !f.fuActive { // orphan fragment: incomplete frame
			f.stats.BadPayloads++
			f.dropCurrent()
			return
		}
		f.addBytes(len(p) - 2)
		if end {
			f.fuActive = false
			if pkt.Marker {
				f.curDone = true
			}
		}

	default: // unsupported / invalid payload type
		f.stats.BadPayloads++
		f.dropCurrent()
	}
}

// Flush terminates the stream, accounting for the last frame. It is
// called on stream end, on idle timeout and on early disconnect.
func (f *FrameReassembler) Flush() {
	if f.hasCur {
		if f.curDone {
			f.stats.Frames++
		} else {
			f.stats.DroppedFrames++
		}
	}
	f.hasCur = false
	f.fuActive = false
	f.skipping = false
}
