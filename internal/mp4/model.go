// Package mp4 scans self-generated fragmented MP4 samples.
//
// The scanner deliberately performs explicit bounds checks before every slice.
// It uses github.com/abema/go-mp4 to unmarshal supported mfhd/tfdt/trun/emsg
// payloads, while retaining avpipe's precise first-unreadable offsets.
package mp4

import (
	"github.com/zld/avpipe/internal/fault"
)

// BoxView is a safely located MP4 box.
type BoxView struct {
	Type         string
	Offset       int
	HeaderSize   int
	DeclaredSize int
	PayloadStart int
	Payload      []byte
	Complete     bool
}

// BoxInfo is a compact box inventory entry.
type BoxInfo struct {
	Type     string `json:"type"`
	Offset   int64  `json:"offset"`
	Size     int64  `json:"size"`
	Complete bool   `json:"complete"`
}

// TimeRange marks a media interval whose boundaries cannot be confirmed.
type TimeRange struct {
	StartTicks int64  `json:"start_ticks"`
	EndTicks   int64  `json:"end_ticks"`
	Timescale  uint32 `json:"timescale"`
	Reason     string `json:"reason"`
}

// SampleInfo records whether one trun-declared sample is fully readable.
type SampleInfo struct {
	Index    int    `json:"index"`
	Offset   int64  `json:"offset"`
	Size     uint32 `json:"size"`
	Readable bool   `json:"readable"`
}

// Segment is a recovered media fragment.
type Segment struct {
	Representation        string       `json:"representation"`
	Sequence              uint32       `json:"sequence"`
	SequenceKnown         bool         `json:"sequence_known"`
	StartTicks            int64        `json:"start_ticks"`
	DurationTicks         uint32       `json:"duration_ticks"`
	Timescale             uint32       `json:"timescale"`
	TimingKnown           bool         `json:"timing_known"`
	Readable              bool         `json:"readable"`
	Indexed               bool         `json:"indexed"`
	RebuiltIndex          bool         `json:"rebuilt_index"`
	BoxOffset             int64        `json:"box_offset"`
	MdatPayloadOffset     int64        `json:"mdat_payload_offset"`
	MdatPayloadAvailable  int          `json:"mdat_payload_available"`
	Samples               []SampleInfo `json:"samples"`
	Corrupt               bool         `json:"corrupt"`
	FirstUnreadableOffset int64        `json:"first_unreadable_offset,omitempty"`
	EMSG                  []EMSGEvent  `json:"emsg,omitempty"`
}

// EMSGEvent is the metadata-relevant subset parsed from an emsg box.
type EMSGEvent struct {
	SchemeIDURI string `json:"scheme_id_uri"`
	Value       string `json:"value"`
	Timescale   uint32 `json:"timescale"`
	PTS         uint64 `json:"pts"`
	Duration    uint32 `json:"duration"`
	ID          uint32 `json:"id"`
	Message     []byte `json:"message,omitempty"`
}

// Report is the result of a corruption-safe MP4 scan.
type Report struct {
	Representation        string         `json:"representation"`
	Timescale             uint32         `json:"timescale"`
	DataSize              int            `json:"data_size"`
	Boxes                 []BoxInfo      `json:"boxes"`
	Segments              []Segment      `json:"segments"`
	IndexEntries          int            `json:"index_entries"`
	IndexedSegments       int            `json:"indexed_segments"`
	RebuiltIndex          int            `json:"rebuilt_index"`
	MissingIndex          bool           `json:"missing_index"`
	UnconfirmedRanges     []TimeRange    `json:"unconfirmed_ranges,omitempty"`
	FirstUnreadableOffset int64          `json:"first_unreadable_offset,omitempty"`
	Faults                []string       `json:"faults,omitempty"`
	StructuredFaults      []*fault.Error `json:"-"`
}

// Corrupted reports whether any truncation or sample isolation occurred.
func (r *Report) Corrupted() bool {
	return r.FirstUnreadableOffset > 0 || len(r.StructuredFaults) > 0
}

func (r *Report) addFault(err *fault.Error) {
	if err == nil {
		return
	}
	r.StructuredFaults = append(r.StructuredFaults, err)
	r.Faults = append(r.Faults, err.Error())
	if err.FirstUnreadable > 0 && (r.FirstUnreadableOffset == 0 || err.FirstUnreadable < r.FirstUnreadableOffset) {
		r.FirstUnreadableOffset = err.FirstUnreadable
	}
}
