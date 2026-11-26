package engine

import (
	"mediatrace/internal/output"
	"mediatrace/internal/timeline"
)

// buildRTPObservations translates RTP log records onto the shared nanosecond
// clock. SSRC->track mappings come from ssrc_map events; each RTP record is
// anchored to the first packet of its stream and converted with the stream's
// clock rate. Records without a mapping are still emitted with track 0 so the
// manifest remains a complete, reviewable record.
func buildRTPObservations(events []timeline.Event) []output.RTPObservation {
	ssrcToTrack := map[uint32]uint64{}
	ssrcRate := map[uint32]uint32{}
	var rtpEvents []timeline.Event
	for _, e := range events {
		switch e.Kind {
		case timeline.KindSSRCMap:
			ssrcToTrack[e.SSRC] = e.Track
			if e.ClockRate != 0 {
				ssrcRate[e.SSRC] = e.ClockRate
			}
		case timeline.KindRTP:
			rtpEvents = append(rtpEvents, e)
		}
	}
	if len(rtpEvents) == 0 {
		return nil
	}
	// First packet per SSRC anchors the RTP clock to its logged instant.
	firstTS := map[uint32]uint32{}
	firstNS := map[uint32]int64{}
	for _, e := range rtpEvents {
		if _, ok := firstTS[e.SSRC]; !ok {
			firstTS[e.SSRC] = e.RTPTS
			firstNS[e.SSRC] = e.AtNS
		}
	}
	out := make([]output.RTPObservation, 0, len(rtpEvents))
	for _, e := range rtpEvents {
		rate := ssrcRate[e.SSRC]
		if rate == 0 {
			rate = timeline.SampleRateFallback
		}
		clock := timeline.RTPClock{
			SSRC:        e.SSRC,
			ClockRateHz: rate,
			FirstRTP:    firstTS[e.SSRC],
			FirstNS:     firstNS[e.SSRC],
		}
		out = append(out, output.RTPObservation{
			SSRC:        e.SSRC,
			TrackNo:     ssrcToTrack[e.SSRC],
			Sequence:    e.Sequence,
			RTPTS:       e.RTPTS,
			ConvertedNS: clock.RTPToNSExact(e.RTPTS),
			LoggedNS:    e.AtNS,
		})
	}
	return out
}
