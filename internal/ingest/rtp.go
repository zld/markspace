package ingest

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/pion/rtp"

	"mediad/internal/core"
	"mediad/internal/diagnose"
)

// RTPRecord is one packet from a length-prefixed RTP stream file. The file
// format is deliberately trivial: a big-endian uint32 payload length followed
// by one serialized RTP packet, repeated until EOF.
type RTPRecord struct {
	Seq       uint16        `json:"seq"`
	Timestamp uint32        `json:"timestamp_raw"`
	ExtTS     int64         `json:"timestamp_extended"`
	Wrapped   bool          `json:"wrapped"`
	Marker    bool          `json:"marker"`
	Payload   uint8         `json:"payload_type"`
	SSRC      uint32        `json:"ssrc"`
	Len       int           `json:"len"`
	Span      core.Span     `json:"span"`
	Clock     core.Rational `json:"clock"`
	SeqJump   int           `json:"seq_jump,omitempty"`
}

// RTPStream is the parsed form of one RTP capture.
type RTPStream struct {
	Asset      string
	ClockRate  int64
	Records    []RTPRecord
	Wraps      int
	GapLoss    int64
	OutOfOrder int
	IssueList  diagnose.Issues
}

// MaxRTPPacket bounds a single packet before pion sees it.
const MaxRTPPacket = 64 * 1024

// ParseRTP parses a length-prefixed RTP capture. Malformed framing or a
// packet whose declared length disagrees with the header is reported at the
// original byte offset; scanning resumes at the next well-framed packet so
// later legal records are never lost.
func ParseRTP(asset *Asset, clockRate int64) (*RTPStream, error) {
	if clockRate <= 0 {
		return nil, errors.New("ingest: RTP clock rate must be positive")
	}
	st := &RTPStream{Asset: asset.Name, ClockRate: clockRate}
	data := asset.Data
	var off int64
	var ext unwrapper
	for off < int64(len(data)) {
		if int64(len(data))-off < 4 {
			st.IssueList.Add(diagnose.Issue{
				Category: diagnose.CatTruncatedCue, Stage: diagnose.StageIngest,
				Format: "rtp", Asset: asset.Name,
				Span:   core.Span{Start: off, End: int64(len(data))},
				Detail: "truncated packet length prefix", Recoverable: false,
			})
			break
		}
		n := int64(binary.BigEndian.Uint32(data[off:]))
		if n <= 0 || n > MaxRTPPacket || off+4+n > int64(len(data)) {
			st.IssueList.Add(diagnose.Issue{
				Category: diagnose.CatTruncatedCue, Stage: diagnose.StageIngest,
				Format: "rtp", Asset: asset.Name,
				Span:   core.Span{Start: off, End: int64(len(data))},
				Detail: fmt.Sprintf("bad packet length %d at offset %d", n, off),
			})
			break
		}
		pktBytes := data[off+4 : off+4+n]
		span := core.Span{Start: off, End: off + 4 + n}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(pktBytes); err != nil {
			st.IssueList.Add(diagnose.Issue{
				Category: diagnose.CatTruncatedCue, Stage: diagnose.StageIngest,
				Format: "rtp", Asset: asset.Name, Span: span,
				Detail: "invalid RTP packet: " + err.Error(), Recoverable: true,
			})
			off += 4 + n
			continue
		}
		extTS, wrapped := ext.extend(pkt.Timestamp)
		rec := RTPRecord{
			Seq:       pkt.SequenceNumber,
			Timestamp: pkt.Timestamp,
			ExtTS:     extTS,
			Wrapped:   wrapped,
			Marker:    pkt.Marker,
			Payload:   pkt.PayloadType,
			SSRC:      pkt.SSRC,
			Len:       int(n),
			Span:      span,
			Clock:     core.Rat(extTS, clockRate),
		}
		if wrapped {
			st.Wraps++
			st.IssueList.Add(diagnose.Issue{
				Category: diagnose.CatRTPWrap, Stage: diagnose.StageIngest,
				Format: "rtp", Asset: asset.Name, Span: span,
				Detail:      fmt.Sprintf("32-bit timestamp wrapped at seq=%d raw=%d", rec.Seq, rec.Timestamp),
				Recoverable: true,
			})
		}
		if len(st.Records) > 0 {
			prev := st.Records[len(st.Records)-1]
			jump := int(int16(rec.Seq - prev.Seq))
			switch {
			case jump == 1:
			case jump == 0:
				st.OutOfOrder++
				st.IssueList.Add(diagnose.Issue{
					Category: diagnose.CatRTPSequence, Stage: diagnose.StageIngest,
					Format: "rtp", Asset: asset.Name, Span: span,
					Detail: fmt.Sprintf("duplicate sequence %d", rec.Seq), Recoverable: true,
				})
			case jump > 1:
				rec.SeqJump = jump
				st.GapLoss += int64(jump - 1)
				st.IssueList.Add(diagnose.Issue{
					Category: diagnose.CatRTPSequence, Stage: diagnose.StageIngest,
					Format: "rtp", Asset: asset.Name, Span: span,
					Detail:      fmt.Sprintf("sequence gap %d -> %d (lost %d)", prev.Seq, rec.Seq, jump-1),
					Recoverable: true,
				})
			default:
				st.OutOfOrder++
				st.IssueList.Add(diagnose.Issue{
					Category: diagnose.CatRTPSequence, Stage: diagnose.StageIngest,
					Format: "rtp", Asset: asset.Name, Span: span,
					Detail:      fmt.Sprintf("out-of-order sequence %d after %d", rec.Seq, prev.Seq),
					Recoverable: true,
				})
			}
		}
		st.Records = append(st.Records, rec)
		off += 4 + n
	}
	return st, nil
}

// unwrapper maintains a 64-bit monotonic extension of a 32-bit RTP clock.
type unwrapper struct {
	started bool
	last    uint32
	ext     int64
}

func (u *unwrapper) extend(ts uint32) (int64, bool) {
	if !u.started {
		u.started = true
		u.last = ts
		u.ext = int64(ts)
		return u.ext, false
	}
	delta := int64(int32(ts - u.last))
	// A small positive signed delta whose new raw value is numerically below
	// the previous one is a wrap. A large negative delta is a genuine
	// regression and is preserved so continuity checks can reject it.
	wrapped := delta > 0 && ts < u.last
	u.ext += delta
	u.last = ts
	return u.ext, wrapped
}

// RTPTimeContinuity reports whether extended timestamps never regress.
func (s *RTPStream) RTPTimeContinuity() bool {
	for i := 1; i < len(s.Records); i++ {
		if s.Records[i].ExtTS < s.Records[i-1].ExtTS {
			return false
		}
	}
	return true
}
