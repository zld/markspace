// Package index validates keyframe/sample index records against the streams
// they describe. Every violation is reported with the first byte offset
// involved; valid records are returned for downstream stages.
package index

import (
	"math"

	"mediarc/internal/model"

	"github.com/pion/rtp"
)

// Record is one index entry: a sample located in a stream, optionally
// carrying the raw RTP packet header it arrived with.
type Record struct {
	Codec     string `json:"codec"`
	Track     int    `json:"track"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
	PTS       int64  `json:"pts"`
	Timescale int64  `json:"timescale"`
	RTP       []byte `json:"rtp,omitempty"`
}

// Valid is a record that passed validation, with any RTP header parsed.
type Valid struct {
	Record
	RTPTimestamp uint32
	HasRTP       bool
}

// Validate checks each record against the stream size and the set of known
// AU boundary offsets. Out-of-range, overflowing and misaligned records are
// reported at the first byte offset involved; validation continues so the
// report lists every violation.
func Validate(recs []Record, streamSize int64, boundaries map[int64]bool) ([]Valid, []*model.Error) {
	var ok []Valid
	var errs []*model.Error
	for i, r := range recs {
		bad := func(code model.Code, off int64, msg string) {
			errs = append(errs, &model.Error{Code: code, Track: r.Track, Offset: off, Msg: msg})
		}
		switch {
		case r.Offset < 0 || r.Length < 0:
			bad(model.ErrOutOfRange, max(r.Offset, 0), "negative offset or length")
			continue
		case r.Length > math.MaxInt64-r.Offset:
			bad(model.ErrOverflow, r.Offset, "offset+length overflows int64")
			continue
		case r.Offset+r.Length > streamSize:
			bad(model.ErrOutOfRange, r.Offset, "record extends past end of stream")
			continue
		case boundaries != nil && !boundaries[r.Offset]:
			bad(model.ErrMisaligned, r.Offset, "offset is not an access-unit boundary")
			continue
		}
		v := Valid{Record: recs[i]}
		if len(r.RTP) > 0 {
			var pkt rtp.Packet
			if err := pkt.Unmarshal(r.RTP); err != nil {
				bad(model.ErrRTP, r.Offset, "malformed RTP header: "+err.Error())
				continue
			}
			v.RTPTimestamp, v.HasRTP = pkt.Timestamp, true
		}
		ok = append(ok, v)
	}
	return ok, errs
}
