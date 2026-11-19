package demux

import (
	"sort"

	"tsinspect/internal/program"
	"tsinspect/internal/transport"
)

// Boundary marks the output position at which a PMT version became the one
// used for slicing decisions.
type Boundary struct {
	OutputPacket int64 `json:"output_packet"`
	// InputOffset is the offset of the packet whose processing made this
	// version the effective one (normally the PMT packet itself).
	InputOffset   int64    `json:"input_offset"`
	ProgramNumber uint16   `json:"program_number"`
	PMTPID        uint16   `json:"pmt_pid"`
	Version       uint8    `json:"version"`
	PCRPID        uint16   `json:"pcr_pid"`
	StreamPIDs    []uint16 `json:"stream_pids"`
}

// SliceStats describes what slicing produced.
type SliceStats struct {
	Enabled         bool
	Program         uint16
	PacketsWritten  int64
	BufferedFlushed int64
	BufferedDropped int64
	BufferOverflows int64
	PMTPackets      int64
	ESPackets       int64
	VersionSeen     uint8
	HaveVersion     bool
	Boundaries      []Boundary
	// NoTableDropped counts packets discarded because the program table was
	// never available inside the buffer budget.
	NoTableDropped int64
	// ProgramEnded is set if the program disappeared from a newer PAT.
	ProgramEnded bool
	// ProgramAbsent is set when the effective PAT never contained the
	// requested program.
	ProgramAbsent bool
}

// stageBoundary prepares the boundary record for the current mapping.
func (s *Slicer) stageBoundary() {
	pmt := s.curPMT
	if pmt == nil {
		return
	}
	b := &Boundary{
		InputOffset:   s.boundaryOffset,
		ProgramNumber: s.program,
		PMTPID:        pmt.PID,
		Version:       pmt.Version,
		PCRPID:        pmt.PCRPID,
		StreamPIDs:    make([]uint16, 0, len(pmt.Streams)),
	}
	for _, es := range pmt.Streams {
		b.StreamPIDs = append(b.StreamPIDs, es.PID)
	}
	sort.Slice(b.StreamPIDs, func(i, j int) bool { return b.StreamPIDs[i] < b.StreamPIDs[j] })
	s.boundaryDue = b
}

// noteBoundaryIfDue appends a boundary once the first packet under the
// current mapping is about to be written.
func (s *Slicer) noteBoundaryIfDue() {
	if s.boundaryDue == nil {
		return
	}
	b := *s.boundaryDue
	b.OutputPacket = s.stats.PacketsWritten
	s.stats.Boundaries = append(s.stats.Boundaries, b)
	s.boundaryDue = nil
}

// firstRelevant finds the earliest cached packet belonging to pmt.
func firstRelevant(pkts []*transport.Packet, pmt *program.PMTState) *transport.Packet {
	streams := make(map[uint16]bool, len(pmt.Streams))
	for _, es := range pmt.Streams {
		streams[es.PID] = true
	}
	for _, pkt := range pkts {
		if pkt.PID == pmt.PID || streams[pkt.PID] {
			return pkt
		}
	}
	return nil
}

// Stats returns a snapshot of slicing statistics. A nil output writer still
// fills the counters, so -json without -out reports what would be written.
func (s *Slicer) Stats() SliceStats {
	st := s.stats
	st.Boundaries = append([]Boundary(nil), s.stats.Boundaries...)
	return st
}

// Err returns the first write failure encountered, if any.
func (s *Slicer) Err() error { return s.writeErr }
