package fixture

type Manifest struct {
	Version        int     `json:"version"`
	Seed           int64   `json:"seed"`
	Corruption     string  `json:"corruption,omitempty"`
	SessionStartNs int64   `json:"session_start_ns"`
	TargetBps      int64   `json:"target_bps,omitempty"`
	Entries        []Entry `json:"entries"`
}

type Entry struct {
	Camera    string      `json:"camera"`
	File      string      `json:"file"`
	Tracks    []TrackSpec `json:"tracks"`
	Recording Interval    `json:"recording"`
	Editorial Interval    `json:"editorial"`
}

type TrackSpec struct {
	TrackID   uint32 `json:"track_id"`
	Kind      string `json:"kind"`
	Timescale uint32 `json:"timescale"`
	StartNs   int64  `json:"start_ns"`
}

type Interval struct {
	StartNs int64 `json:"start_ns"`
	EndNs   int64 `json:"end_ns"`
}

// Corruption names accepted by Generate; "" produces clean media.
const (
	CorruptTruncatedTable = "truncated-table"
	CorruptEditOverrun    = "edit-overrun"
	CorruptRTPWrap        = "rtp-wrap"
	CorruptMissingSync    = "missing-sync-flags"
	CorruptSparseKey      = "sparse-keyframes"
	CorruptDTSRegression  = "dts-regression"
	CorruptNone           = ""
)

// Corruptions lists every accepted corruption name.
func Corruptions() []string {
	return []string{CorruptNone, CorruptTruncatedTable, CorruptEditOverrun, CorruptRTPWrap, CorruptMissingSync, CorruptSparseKey, CorruptDTSRegression}
}

// Generate writes a complete fixture set into dir. The manifest lists one
