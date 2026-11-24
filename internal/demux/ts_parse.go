package demux

import (
	"mediapipeline/internal/fault"
	"mediapipeline/internal/track"
)

// ParseTS demuxes an MPEG-TS byte slice.  It works in two passes so the
// elementary stream map is final before any PES is interpreted: pass one
// validates packet framing, continuity and PSI; pass two reassembles PES
// packets at their exact source offsets.
func ParseTS(data []byte, opts Options) (*Media, error) {
	media := &Media{Format: "ts"}
	psi, err := scanTS(data, media, opts)
	if err != nil {
		return nil, err
	}
	if psi.pmtPID == 0 {
		return nil, fault.New(fault.StageDemux, "ts", fault.OpIndexIncomplete, 0,
			"stream has no PAT/PMT program information")
	}
	if len(psi.streams) == 0 {
		return nil, fault.New(fault.StageDemux, "ts", fault.OpIndexIncomplete, 0,
			"PMT declares no elementary streams")
	}
	tracks, events, err := assemblePES(data, psi, media)
	if err != nil {
		return nil, err
	}
	media.Tracks = tracks
	media.Events = events
	for i := range media.Events {
		media.Events[i].Seq = i
	}
	return media, nil
}

type tsStream struct {
	pid   uint16
	typ   byte
	kind  track.Kind
	codec string
}

type tsPSI struct {
	pmtPID  uint16
	pmtPIDs map[uint16]bool
	streams map[uint16]tsStream
}
