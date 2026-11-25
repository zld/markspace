package ingest

import (
	"fmt"
	"sync"

	"github.com/pion/rtp"

	"medpipe/internal/fault"
)

// RTPHeader is the stable subset of a cross-node RTP header used by medpipe.
type RTPHeader struct {
	Version        uint8
	Marker         bool
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	CSRCCount      uint8
}

// RTPReport describes sequence and timestamp checks for one packet.
type RTPReport struct {
	Header          RTPHeader
	SequenceStatus  string
	TimestampStatus string
	Failures        []fault.Failure
}

type rtpState struct {
	lastSeq       uint16
	haveSeq       bool
	lastTimestamp uint32
	haveTS        bool
}

// RTPTracker keeps per-SSRC RTP ordering state. It is safe for concurrent use.
type RTPTracker struct {
	mu      sync.Mutex
	sources map[uint32]*rtpState
}

// NewRTPTracker creates an empty sequence/timestamp tracker.
func NewRTPTracker() *RTPTracker {
	return &RTPTracker{sources: make(map[uint32]*rtpState)}
}

// Inspect parses one raw RTP packet and updates its SSRC state. inputOffset is
// the packet's location in a captured metadata stream; standalone datagrams
// may use 0. Parse errors retain that offset.
func (t *RTPTracker) Inspect(data []byte, inputOffset int64, trackID int) (*RTPReport, error) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(data); err != nil {
		return nil, fault.New(fault.StageRTP, "invalid_rtp_packet", inputOffset, trackID, err)
	}
	h := pkt.Header
	rep := &RTPReport{
		Header: RTPHeader{
			Version: h.Version, Marker: h.Marker, PayloadType: h.PayloadType,
			SequenceNumber: h.SequenceNumber, Timestamp: h.Timestamp,
			SSRC: h.SSRC, CSRCCount: uint8(len(h.CSRC)),
		},
		SequenceStatus:  "ok",
		TimestampStatus: "ok",
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.sources[h.SSRC]
	if st == nil {
		st = &rtpState{}
		t.sources[h.SSRC] = st
	}

	if st.haveSeq {
		switch delta := int16(h.SequenceNumber - st.lastSeq); {
		case h.SequenceNumber == st.lastSeq:
			rep.SequenceStatus = "duplicate"
			rep.addIssue("rtp_duplicate_sequence", inputOffset, trackID,
				fmt.Sprintf("ssrc=%d sequence=%d", h.SSRC, h.SequenceNumber))
		case delta == 1:
			rep.SequenceStatus = "continuous"
		case delta > 1:
			rep.SequenceStatus = "gap"
			rep.addIssue("rtp_sequence_gap", inputOffset, trackID,
				fmt.Sprintf("ssrc=%d last=%d now=%d", h.SSRC, st.lastSeq, h.SequenceNumber))
		default:
			rep.SequenceStatus = "reordered"
			rep.addIssue("rtp_reordered_sequence", inputOffset, trackID,
				fmt.Sprintf("ssrc=%d last=%d now=%d", h.SSRC, st.lastSeq, h.SequenceNumber))
		}
	}
	st.lastSeq = h.SequenceNumber
	st.haveSeq = true

	if st.haveTS {
		switch delta := int32(h.Timestamp - st.lastTimestamp); {
		case delta == 0:
			rep.TimestampStatus = "same"
		case delta > 0:
			rep.TimestampStatus = "monotonic"
		default:
			rep.TimestampStatus = "regressed"
			rep.addIssue("rtp_timestamp_regression", inputOffset, trackID,
				fmt.Sprintf("ssrc=%d last=%d now=%d", h.SSRC, st.lastTimestamp, h.Timestamp))
		}
	}
	st.lastTimestamp = h.Timestamp
	st.haveTS = true
	return rep, nil
}

func (r *RTPReport) addIssue(code string, offset int64, track int, msg string) {
	r.Failures = append(r.Failures, fault.Failure{
		Stage: fault.StageRTP, Code: code, Offset: offset, TrackID: track, Message: msg,
	})
}
