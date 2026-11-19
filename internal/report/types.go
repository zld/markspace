// Package report renders a deterministic JSON summary of one inspection pass.
// Every list is sorted explicitly; the struct field order fixes key order.
package report

import "tsinspect/internal/demux"

// Summary is the top-level JSON document.
type Summary struct {
	Input             string             `json:"input"`
	PacketsProcessed  int64              `json:"packets_processed"`
	BytesProcessed    int64              `json:"bytes_processed"`
	PATVersionChanges int                `json:"pat_version_changes"`
	PMTVersionChanges int                `json:"pmt_version_changes"`
	PIDMappings       []PIDMapping       `json:"pid_mappings"`
	ElementaryStreams []ElementaryStream `json:"elementary_streams"`
	Programs          []ProgramReport    `json:"programs"`
	TableEvents       []TableEvent       `json:"table_events"`
	Anomalies         Anomalies          `json:"anomalies"`
	Slice             *SliceReport       `json:"slice,omitempty"`
	Errors            []string           `json:"errors"`
}

// PIDMapping is one elementary PID mapped to its program.
type PIDMapping struct {
	ProgramNumber     uint16 `json:"program_number"`
	PID               uint16 `json:"pid"`
	StreamType        uint8  `json:"stream_type"`
	PacketCount       int64  `json:"packet_count"`
	DiscontinuitySeen bool   `json:"discontinuity_seen"`
}

// ElementaryStream is one declared stream with its packet accounting.
type ElementaryStream struct {
	ProgramNumber     uint16 `json:"program_number"`
	PID               uint16 `json:"pid"`
	StreamType        uint8  `json:"stream_type"`
	PacketCount       int64  `json:"packet_count"`
	DiscontinuitySeen bool   `json:"discontinuity_seen"`
}

// ProgramReport summarises one program's effective table.
type ProgramReport struct {
	ProgramNumber uint16   `json:"program_number"`
	PMTPID        uint16   `json:"pmt_pid"`
	PCRPID        uint16   `json:"pcr_pid"`
	Version       uint8    `json:"version"`
	StreamPIDs    []uint16 `json:"stream_pids"`
}

// TableEvent is a lifecycle transition surfaced for diagnostics.
type TableEvent struct {
	Kind          string `json:"kind"`
	ProgramNumber uint16 `json:"program_number"`
	PID           uint16 `json:"pid,omitempty"`
	Version       uint8  `json:"version"`
	Status        string `json:"status"`
	Detail        string `json:"detail"`
}

// Anomalies is the global continuity summary.
type Anomalies struct {
	Duplicates        int64 `json:"duplicates"`
	Jumps             int64 `json:"counter_jumps"`
	TransportErrors   int64 `json:"transport_errors"`
	AFOnly            int64 `json:"af_only_packets"`
	UnknownGaps       int64 `json:"unjudgeable"`
	PIDsDiscontinuous int64 `json:"pids_with_discontinuity"`
	CRCErrors         int64 `json:"crc_errors"`
	SectionErrors     int64 `json:"section_errors"`
	OrphanPMTs        int64 `json:"orphan_pmts"`
	IgnoredTables     int64 `json:"ignored_tables"`
}

// SliceReport describes the written program slice.
type SliceReport struct {
	Program         uint16           `json:"program_number"`
	PacketsWritten  int64            `json:"packets_written"`
	PMTPackets      int64            `json:"pmt_packets"`
	ESPackets       int64            `json:"elementary_packets"`
	BufferedFlushed int64            `json:"buffered_flushed"`
	BufferedDropped int64            `json:"buffered_dropped"`
	BufferOverflows int64            `json:"buffer_overflows"`
	ProgramEnded    bool             `json:"program_ended"`
	ProgramAbsent   bool             `json:"program_absent"`
	BoundaryCount   int              `json:"version_boundary_count"`
	Boundaries      []demux.Boundary `json:"version_boundaries"`
}
