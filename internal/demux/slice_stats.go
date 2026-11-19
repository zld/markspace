package demux

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
