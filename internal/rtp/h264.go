package rtp

import (
	"errors"
	"fmt"
	"sync"

	"github.com/pion/rtp"
)

// MaxFrameSize is the largest access unit the ingest tool accepts (8 MiB).
// A frame that grows past this limit is discarded and counted.
const MaxFrameSize = 8 * 1024 * 1024

// H264 NAL unit type values from RFC 6184.
const (
	nalTypeSTAPA = 24
	nalTypeFUA   = 28
)

// FrameKey identifies an access unit by its RTP triplet. Frames with
// different SSRC, payload type or timestamp are reassembled independently.
type FrameKey struct {
	SSRC        uint32
	PayloadType uint8
	Timestamp   uint32
}

// Frame is a reassembled H.264 access unit: an ordered list of NAL units.
type Frame struct {
	Key   FrameKey
	NALUs [][]byte
	Size  int
}

// ReassemblyStats collects reassembly counters.
type ReassemblyStats struct {
	ReassembledFrames uint64
	DiscardedFrames   uint64
	MissingFragments  uint64
	IllegalFUHeaders  uint64
	OversizedFrames   uint64
	MalformedPackets  uint64
}

// Reassembler rebuilds H.264 access units from RTP packets.
//
// It supports single NAL unit packets, STAP-A aggregation packets and FU-A
// fragmentation packets. Incomplete or invalid access units are discarded.
type Reassembler struct {
	mu      sync.Mutex
	maxSize int

	// pending accumulates NAL units per access unit until the RTP marker.
	pending map[FrameKey]*accessUnit
	// fu holds an in-progress FU-A reassembly.
	fu map[FrameKey]*fuAssembly

	stats ReassemblyStats
}

type accessUnit struct {
	nals    [][]byte
	size    int
	invalid bool
}

type fuAssembly struct {
	header   byte // reconstructed NAL header (ref idc + type)
	data     []byte
	started  bool
	oversize bool
	lastSeq  uint16
	haveSeq  bool
}

// NewReassembler creates a reassembler with the 8 MiB frame limit.
func NewReassembler() *Reassembler {
	return &Reassembler{
		maxSize: MaxFrameSize,
		pending: map[FrameKey]*accessUnit{},
		fu:      map[FrameKey]*fuAssembly{},
	}
}

// Process consumes one RTP packet. It returns a complete Frame when the
// packet carrying the RTP marker closes a valid access unit, otherwise nil.
func (r *Reassembler) Process(pkt *rtp.Packet) (*Frame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := FrameKey{pkt.SSRC, pkt.PayloadType, pkt.Timestamp}
	au := r.pending[key]
	if au == nil {
		au = &accessUnit{}
		r.pending[key] = au
	}

	if au.invalid {
		// the access unit was already discarded; swallow later packets.
		if pkt.Marker {
			delete(r.pending, key)
		}
		return nil, nil
	}

	if err := r.processPayload(key, au, pkt); err != nil {
		r.discard(key, au)
		switch {
		case errors.Is(err, errOversize):
			r.stats.OversizedFrames++
		case errors.Is(err, errIllegalFU):
			r.stats.IllegalFUHeaders++
		case errors.Is(err, errMissingFragment):
			r.stats.MissingFragments++
		default:
			r.stats.MalformedPackets++
		}
		if pkt.Marker {
			delete(r.pending, key)
		}
		return nil, err
	}

	if !pkt.Marker {
		return nil, nil
	}

	// a marker while a FU-A is still open means the ending fragment is gone.
	if asm := r.fu[key]; asm != nil {
		r.discard(key, au)
		if asm.oversize {
			r.stats.OversizedFrames++
		} else {
			r.stats.MissingFragments++
		}
		delete(r.pending, key)
		return nil, fmt.Errorf("rtp: access unit %+v closed with an unfinished FU-A", key)
	}

	frame := r.emit(key, au)
	delete(r.pending, key)
	return frame, nil
}

// Flush forces out every buffered access unit. Units whose FU-A never
// completed are discarded. It models an end-of-stream / timeout drain.
func (r *Reassembler) Flush() []*Frame {
	r.mu.Lock()
	defer r.mu.Unlock()

	var frames []*Frame
	for key, au := range r.pending {
		if au.invalid {
			delete(r.pending, key)
			continue
		}
		if asm := r.fu[key]; asm != nil {
			if asm.oversize {
				r.stats.OversizedFrames++
			} else {
				r.stats.MissingFragments++
			}
			r.discard(key, au)
			delete(r.pending, key)
			continue
		}
		if len(au.nals) > 0 {
			frames = append(frames, r.buildFrame(key, au))
		}
		delete(r.pending, key)
	}
	return frames
}

// Stats returns a copy of the reassembly counters.
func (r *Reassembler) Stats() ReassemblyStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

var (
	errMalformed       = errors.New("rtp: malformed H264 payload")
	errIllegalFU       = errors.New("rtp: illegal FU-A start/end markers")
	errMissingFragment = errors.New("rtp: missing FU-A fragment")
	errOversize        = errors.New("rtp: access unit exceeds 8 MiB")
)

func (r *Reassembler) processPayload(key FrameKey, au *accessUnit, pkt *rtp.Packet) error {
	payload := pkt.Payload
	if len(payload) == 0 {
		return fmt.Errorf("%w: empty payload", errMalformed)
	}

	nalType := payload[0] & 0x1F
	switch {
	case nalType >= 1 && nalType < nalTypeSTAPA:
		return r.appendNAL(key, au, payload)
	case nalType == nalTypeSTAPA:
		return r.processSTAPA(key, au, payload)
	case nalType == nalTypeFUA:
		return r.processFUA(key, au, pkt)
	default:
		// STAP-B / FU-B / reserved types are not valid in this profile.
		return fmt.Errorf("%w: unsupported NAL type %d", errMalformed, nalType)
	}
}

func (r *Reassembler) appendNAL(key FrameKey, au *accessUnit, nal []byte) error {
	if au.size+len(nal) > r.maxSize {
		return fmt.Errorf("%w: key=%+v size=%d", errOversize, key, au.size+len(nal))
	}
	cp := make([]byte, len(nal))
	copy(cp, nal)
	au.nals = append(au.nals, cp)
	au.size += len(cp)
	return nil
}

func (r *Reassembler) emit(key FrameKey, au *accessUnit) *Frame {
	if au.invalid || len(au.nals) == 0 {
		return nil
	}
	r.stats.ReassembledFrames++
	return r.buildFrame(key, au)
}

func (r *Reassembler) buildFrame(key FrameKey, au *accessUnit) *Frame {
	return &Frame{Key: key, NALUs: au.nals, Size: au.size}
}

// discard marks the access unit invalid and counts it once. The tombstone
// entry stays in the map until the marker or Flush removes it, so later
// packets belonging to the same unit do not inflate the counters.
func (r *Reassembler) discard(key FrameKey, au *accessUnit) {
	delete(r.fu, key)
	if !au.invalid {
		au.invalid = true
		r.stats.DiscardedFrames++
	}
}
