// Package report builds the stable JSON/text summaries emitted by tsinspect.
package report

// Stream is one elementary stream with its observed packet count.
// Stream is one elementary stream with its observed packet count.
type Stream struct {
	ProgramNumber    uint16 `json:"program_number"`
	StreamType       uint8  `json:"stream_type"`
	StreamTypeHex    string `json:"stream_type_hex"`
	PID              uint16 `json:"pid"`
	PacketCount      int64  `json:"packet_count"`
	ContinuityErrors int64  `json:"continuity_errors"`
	Description      string `json:"description"`
}

// Program is one program from the effective tables.
type Program struct {
	ProgramNumber uint16   `json:"program_number"`
	PMTPID        uint16   `json:"pmt_pid"`
	PCRPID        uint16   `json:"pcr_pid"`
	PMTVersion    int      `json:"pmt_version"`
	PCRPresent    bool     `json:"pcr_present"`
	Streams       []Stream `json:"streams"`
	Active        bool     `json:"active"`
}

// PIDReport is a per-PID counter row.
type PIDReport struct {
	PID              uint16 `json:"pid"`
	Packets          int64  `json:"packets"`
	PayloadPackets   int64  `json:"payload_packets"`
	AdaptationOnly   int64  `json:"adaptation_only_packets"`
	ContinuityErrors int64  `json:"continuity_errors"`
	Duplicates       int64  `json:"duplicates"`
	Discontinuities  int64  `json:"discontinuities"`
	TransportErrors  int64  `json:"transport_errors"`
	LostPackets      int64  `json:"lost_packets"`
	Kind             string `json:"kind"`
}

// Summary is the complete, deterministically ordered inspection result.
type Summary struct {
	InputPath            string      `json:"input"`
	PrefixOffset         int64       `json:"prefix_offset_bytes"`
	Packets              int64       `json:"packets"`
	PacketBytes          int64       `json:"packet_bytes"`
	TrailingBytes        int64       `json:"trailing_bytes"`
	TrailingPackets      int64       `json:"trailing_packets"`
	PATVersionChanges    int         `json:"pat_version_changes"`
	PMTVersionChanges    int         `json:"pmt_version_changes"`
	PATDuplicates        int         `json:"pat_duplicates"`
	PMTDuplicates        int         `json:"pmt_duplicates"`
	FutureSections       int         `json:"future_sections"`
	PendingDiscards      int         `json:"pending_table_discards"`
	Sections             int         `json:"sections_parsed"`
	SectionCRCErrors     int         `json:"section_crc_errors"`
	SectionLengthErrors  int         `json:"section_length_errors"`
	SectionsDiscarded    int         `json:"sections_discarded"`
	ContinuityErrors     int64       `json:"continuity_errors"`
	Duplicates           int64       `json:"duplicates"`
	Discontinuities      int64       `json:"discontinuities"`
	TransportErrors      int64       `json:"transport_errors"`
	LostPackets          int64       `json:"lost_packets"`
	PATVersion           *int        `json:"pat_version"`
	PATTransportStreamID *uint16     `json:"pat_transport_stream_id"`
	ProgramToPMTPID      map[int]int `json:"program_to_pmt_pid"`
	PCRPIDByProgram      map[int]int `json:"pcr_pid_by_program"`
	Programs             []Program   `json:"programs"`
	Streams              []Stream    `json:"streams"`
	PIDs                 []PIDReport `json:"pids"`
}
