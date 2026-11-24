package rtp

import (
	"fmt"

	"github.com/pion/rtp"

	"medcheck/internal/clock"
)

// StreamForPayloadType maps the small synthetic payload-type range used by
// medcheck fixtures to the shared stream identifiers.
func StreamForPayloadType(pt uint8) clock.StreamID {
	switch {
	case pt >= 96 && pt <= 109 && pt%2 == 0:
		return clock.StreamVideo
	case pt >= 96 && pt <= 111:
		return clock.StreamAudio
	default:
		return ""
	}
}

// Packet is a parsed RTP packet plus capture-location metadata.
type Packet struct {
	Packet      *rtp.Packet
	Raw         []byte
	Stream      clock.StreamID
	Offset      int64
	ExtSequence uint64
	Kind        Disposition
	Missing     []uint64
}

// Stats contains ingest counters.
type Stats struct {
	Packets    int `json:"packets"`
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Reordered  int `json:"reordered"`
	Missing    int `json:"missing"`
	Invalid    int `json:"invalid"`
}

// Ingestor demultiplexes SSRCs and classifies RTP sequence behavior.
type Ingestor struct {
	streams  map[uint32]clock.StreamID
	trackers map[uint32]*Tracker
	Stats    Stats
	Errors   []clock.MediaError
}

// NewIngestor creates an empty ingestor.
func NewIngestor() *Ingestor {
	return &Ingestor{
		streams:  make(map[uint32]clock.StreamID),
		trackers: make(map[uint32]*Tracker),
	}
}

// Register binds an SSRC to audio or video.
func (in *Ingestor) Register(ssrc uint32, stream clock.StreamID) {
	in.streams[ssrc] = stream
}

// Tracker returns the sequence tracker for an SSRC, creating it if needed.
func (in *Ingestor) Tracker(ssrc uint32) *Tracker {
	tr := in.trackers[ssrc]
	if tr == nil {
		tr = NewTracker(ssrc)
		in.trackers[ssrc] = tr
	}
	return tr
}

// SnapshotSequences exports per-SSRC sequence state for checkpoints.
func (in *Ingestor) SnapshotSequences() (map[uint32]uint64, map[uint32][]uint64) {
	max := make(map[uint32]uint64, len(in.trackers))
	seen := make(map[uint32][]uint64, len(in.trackers))
	for ssrc, tr := range in.trackers {
		m, s := tr.Snapshot()
		max[ssrc] = m
		seen[ssrc] = s
	}
	return max, seen
}

// RestoreSequences restores per-SSRC sequence state.
func (in *Ingestor) RestoreSequences(max map[uint32]uint64, seen map[uint32][]uint64) {
	for ssrc, m := range max {
		tr := in.Tracker(ssrc)
		tr.Restore(m, seen[ssrc])
	}
}

// Ingest parses and classifies one raw RTP datagram located at offset.
func (in *Ingestor) Ingest(raw []byte, offset int64) (Packet, []clock.MediaError) {
	in.Stats.Packets++
	var pkt rtp.Packet
	var err error
	if len(raw) > 0 && raw[0]>>6 != 2 {
		err = fmt.Errorf("invalid RTP version %d", raw[0]>>6)
	} else {
		err = pkt.Unmarshal(raw)
	}
	if err != nil {
		in.Stats.Invalid++
		stream, ssrc := partialHeader(raw)
		me := clock.MediaError{
			Code:        clock.CodeInvalidPacket,
			Stream:      stream,
			SSRC:        ssrc,
			Sequence:    partialSequence(raw),
			InputOffset: offset,
			Message:     err.Error(),
		}
		in.Errors = append(in.Errors, me)
		return Packet{Stream: stream, Offset: offset, Kind: DispositionInvalid}, []clock.MediaError{me}
	}

	stream := in.streams[pkt.SSRC]
	if stream == "" {
		stream = StreamForPayloadType(pkt.PayloadType)
	}
	if stream == "" {
		stream = clock.StreamID("unknown")
	}
	report := in.Tracker(pkt.SSRC).Accept(pkt.SequenceNumber)
	var errs []clock.MediaError
	switch report.Kind {
	case DispositionDuplicate:
		in.Stats.Duplicates++
		errs = append(errs, clock.MediaError{
			Code: clock.CodeDuplicate, Stream: stream, SSRC: pkt.SSRC,
			Sequence: report.ExtSequence, InputOffset: offset,
		})
	case DispositionOutOfOrder:
		in.Stats.Reordered++
		errs = append(errs, clock.MediaError{
			Code: clock.CodeOutOfOrder, Stream: stream, SSRC: pkt.SSRC,
			Sequence: report.ExtSequence, InputOffset: offset,
		})
	case DispositionNew:
		in.Stats.Accepted++
	}
	for _, seq := range report.Missing {
		in.Stats.Missing++
		errs = append(errs, clock.MediaError{
			Code: clock.CodeMissingPacket, Stream: stream, SSRC: pkt.SSRC,
			Sequence: seq, InputOffset: offset,
			Message: "sequence gap detected before current packet",
		})
	}
	in.Errors = append(in.Errors, errs...)
	return Packet{
		Packet: &pkt, Raw: raw, Stream: stream, Offset: offset,
		ExtSequence: report.ExtSequence, Kind: report.Kind, Missing: report.Missing,
	}, errs
}

func partialHeader(raw []byte) (clock.StreamID, uint32) {
	if len(raw) >= 12 {
		ssrc := uint32(raw[8])<<24 | uint32(raw[9])<<16 | uint32(raw[10])<<8 | uint32(raw[11])
		return StreamForPayloadType(raw[1] & 0x7f), ssrc
	}
	return "unknown", 0
}

func partialSequence(raw []byte) uint64 {
	if len(raw) >= 4 {
		return uint64(uint16(raw[2])<<8 | uint16(raw[3]))
	}
	return 0
}
