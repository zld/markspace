package adapter

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/pion/rtp"

	"mediad/internal/media"
)

// rtpcapMagic identifies the local packet-capture container: a sequence of
// [arrival_ns uint64][length uint16][RTP packet] records.
var rtpcapMagic = []byte("RTPC")

// rtpClockRate is the RTP media clock used by the capture fixtures.
const rtpClockRate = 90000

// RTPParser demultiplexes RTP packet captures, expanding 32-bit timestamp
// wrap and estimating per-source clock drift.
type RTPParser struct{}

func (RTPParser) Name() string    { return "rtpcap" }
func (RTPParser) Version() string { return ParserAPIVersion }

func (RTPParser) Probe(head []byte) int {
	if len(head) >= 4 && string(head[:4]) == string(rtpcapMagic) {
		return 100
	}
	return 0
}

// Parse reads capture records, unwraps each source's timestamp clock and
// emits one track per SSRC. Records are consumed as a stream — raw packets
// are not retained beyond their own event. Sequence gaps become
// recoverable media.Gap entries; reordered packets around the wrap point
// are re-sequenced by their expanded timestamps.
func (RTPParser) Parse(r io.ReadSeeker) (*Stream, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(r, head); err != nil || string(head) != string(rtpcapMagic) {
		return nil, media.New(media.PhaseDemux, -1, 0, "bad rtpcap magic", nil)
	}

	type src struct {
		track     *Track
		unwrap    *media.Unwrapper
		drift     *media.DriftEstimator
		lastFedTS int64 // last media time fed to the drift estimator
		seen      []uint16
	}
	srcs := map[uint32]*src{}
	st := &Stream{}

	hdr := make([]byte, 10)
	for rec := 0; ; rec++ {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, media.New(media.PhaseDemux, -1, -1, "record header", err)
		}
		n := binary.BigEndian.Uint16(hdr[8:10])
		raw := make([]byte, n)
		if _, err := io.ReadFull(r, raw); err != nil {
			// Truncated tail record: keep what parsed cleanly.
			break
		}
		arrival := int64(binary.BigEndian.Uint64(hdr[0:8]))
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, media.New(media.PhaseDemux, -1, int64(rec),
				fmt.Sprintf("rtp unmarshal: %v", err), nil)
		}
		s := srcs[pkt.SSRC]
		if s == nil {
			s = &src{
				track: &Track{
					ID:        len(srcs) + 1,
					Type:      media.TrackAudio,
					Codec:     fmt.Sprintf("rtp/pt%d", pkt.PayloadType),
					Timescale: rtpClockRate,
				},
				unwrap: media.NewUnwrapper(32),
				drift:  media.NewDriftEstimator(5_000_000), // 5ms
			}
			srcs[pkt.SSRC] = s
			st.Tracks = append(st.Tracks, s.track)
		}
		ext := s.unwrap.Unwrap(uint64(pkt.Timestamp))
		mt := media.TicksToNanos(int64(ext), rtpClockRate)
		// Reordered packets (e.g. swapped around the wrap) would look like
		// a backwards clock jump to the estimator; feed it only forward-
		// moving observations so reordering cannot poison the fit.
		if s.lastFedTS == 0 || mt >= s.lastFedTS {
			s.drift.Add(arrival, mt)
			s.lastFedTS = mt
		}
		keyframe := len(pkt.Payload) > 0 && pkt.Payload[0]&1 == 1
		s.track.Events = append(s.track.Events, media.Event{
			TrackID:   s.track.ID,
			Seq:       len(s.track.Events),
			MediaTime: mt,
			Arrival:   arrival,
			Duration:  media.TicksToNanos(rtpClockRate/50, rtpClockRate), // 20ms
			Keyframe:  keyframe,
			Payload:   pkt.Payload,
		})
		s.seen = append(s.seen, pkt.SequenceNumber)
	}

	for _, s := range srcs {
		media.SortEventsByTime(s.track.Events)
		for i := range s.track.Events {
			s.track.Events[i].Seq = i
		}
		s.track.DriftSegments = s.drift.Segments()
		st.Gaps = append(st.Gaps, sequenceGaps(s.track.ID, s.seen)...)
	}
	sort.Slice(st.Tracks, func(i, j int) bool { return st.Tracks[i].ID < st.Tracks[j].ID })
	return st, nil
}

// sequenceGaps reports missing RTP sequence numbers as recoverable gaps.
// Sequence numbers are extended through the 16-bit wrap first, so captures
// longer than one cycle (and reordered packets near the wrap) are handled
// correctly.
func sequenceGaps(trackID int, seen []uint16) []media.Gap {
	if len(seen) == 0 {
		return nil
	}
	u := media.NewUnwrapper(16)
	ext := make([]int64, len(seen))
	for i, s := range seen {
		ext[i] = int64(u.Unwrap(uint64(s)))
	}
	sort.Slice(ext, func(i, j int) bool { return ext[i] < ext[j] })
	present := make(map[int64]bool, len(ext))
	for _, e := range ext {
		present[e] = true
	}
	var gaps []media.Gap
	runStart := int64(-1)
	for x := ext[0]; x <= ext[len(ext)-1]; x++ {
		if !present[x] {
			if runStart < 0 {
				runStart = x
			}
		} else if runStart >= 0 {
			gaps = append(gaps, media.Gap{
				TrackID: trackID, FromSeq: int(runStart), ToSeq: int(x),
				Reason: "rtp sequence loss"})
			runStart = -1
		}
	}
	return gaps
}
