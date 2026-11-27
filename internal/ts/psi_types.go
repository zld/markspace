// Package ts: PSI table models and the version history.
package ts

import (
	"example.com/assetpack/internal/fault"
)

const (
	PIDPAT  = 0x0000
	PIDCAT  = 0x0001
	PIDNull = 0x1FFF

	TableIDPAT = 0x00
	TableIDPMT = 0x02

	StreamTypeMPEG1Audio = 0x03
	StreamTypeMPEG2Audio = 0x04
	StreamTypeAAC        = 0x0F
	StreamTypeAC3        = 0x81
	StreamTypeEAC3       = 0x87
	StreamTypeLPCM       = 0x83
)

// PMTStream is one elementary stream entry of a PMT.
type PMTStream struct {
	StreamType  uint8
	PID         uint16
	Language    string
	Descriptors []Descriptor
}

// Audio reports whether the stream type is one this pipeline can consume.
func (s PMTStream) Audio() bool {
	switch s.StreamType {
	case StreamTypeMPEG1Audio, StreamTypeMPEG2Audio, StreamTypeAAC, StreamTypeAC3, StreamTypeEAC3, StreamTypeLPCM:
		return true
	}
	return false
}

// Descriptor is a raw PMT descriptor.
type Descriptor struct {
	Tag  uint8
	Data []byte
}

// Program is one PAT program entry.
type Program struct {
	Number uint16
	PID    uint16
}

// PMT is a parsed program map table.
type PMT struct {
	ProgramNumber   uint16
	PCRPID          uint16
	Version         uint8
	Streams         []PMTStream
	SectionOffset   int64
	PacketsObserved int
}

// AudioStream returns the first audio elementary stream, if any.
func (m *PMT) AudioStream() (PMTStream, bool) {
	if m == nil {
		return PMTStream{}, false
	}
	for _, s := range m.Streams {
		if s.Audio() {
			return s, true
		}
	}
	return PMTStream{}, false
}

// VersionEvent records one PSI version transition.
type VersionEvent struct {
	Table    string `json:"table"`
	PID      uint16 `json:"pid"`
	From     uint8  `json:"from_version"`
	To       uint8  `json:"to_version"`
	Offset   int64  `json:"offset"`
	Packet   int    `json:"packet_index"`
	PCRPID   uint16 `json:"pcr_pid"`
	Programs int    `json:"programs"`
	Streams  int    `json:"streams"`
	CRCValid bool   `json:"crc_valid"`
	Note     string `json:"note,omitempty"`
}

// PSIResult is the combined PAT/PMT view of a stream.
type PSIResult struct {
	PAT        []Program
	PATVersion uint8
	PMTs       map[uint16]*PMT // keyed by PMT PID
	Current    *PMT            // map active at the end of the stream
	Events     []VersionEvent
	History    []VersionedPMT
	Faults     *fault.List
}

// VersionedPMT is one PMT version together with the byte offset at which it
// became effective.  The history keeps every accepted version so a resume
// point can be resolved against the map that was live at that offset.
type VersionedPMT struct {
	PMT    *PMT
	Offset int64
	Packet int
}

// ActivePMTAt returns the PMT that was in force at a given byte offset.  When
// the offset predates every recorded version, the earliest version wins.
func (r *PSIResult) ActivePMTAt(offset int64) *PMT {
	if r == nil || len(r.History) == 0 {
		return nil
	}
	var active *PMT
	for _, h := range r.History {
		if h.Offset > offset {
			if active == nil {
				return h.PMT
			}
			break
		}
		active = h.PMT
	}
	return active
}

type sectionBuf struct {
	pid      uint16
	data     []byte
	startOff int64
	startPkt int
	span     int // transport packets contributed since the section header
}

// psiParser holds the section assembly state for one pass.
type psiParser struct {
	stream  *Stream
	res     *PSIResult
	partial map[uint16]*sectionBuf
	seenPAT bool
	seenPMT map[uint16]bool
}
