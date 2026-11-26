package rtp

import "example.com/archiveverify/internal/cmaf"

// ArrivalsFromSpecs converts deterministic fixture/manifest packets through
// pion marshal/parse so sequence numbers and timestamps are actually parsed.
func ArrivalsFromSpecs(specs []cmaf.RTPPacketSpec) ([]Arrival, error) {
	out := make([]Arrival, 0, len(specs))
	for _, s := range specs {
		a, err := NewArrival(s.Sequence, s.Timestamp, s.SSRC, s.Marker, s.Payload,
			s.IsRetrans, s.OriginalSeq)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
