package rtp

import "sort"

func sortCaptured(xs []Captured) {
	sort.SliceStable(xs, func(i, j int) bool {
		if xs[i].Packet.SSRC != xs[j].Packet.SSRC {
			return xs[i].Packet.SSRC < xs[j].Packet.SSRC
		}
		return xs[i].Ext < xs[j].Ext
	})
}

func sortGaps(xs []Gap) {
	sort.SliceStable(xs, func(i, j int) bool {
		if xs[i].SSRC != xs[j].SSRC {
			return xs[i].SSRC < xs[j].SSRC
		}
		return xs[i].StartExt < xs[j].StartExt
	})
}

// AssignGapIntervals maps sequence gaps to media time using the nearest
// received packets and a caller-supplied RTP timestamp mapper. For equal
// packet durations, the interval starts at the first missing packet's
// timestamp and ends at the next received packet's timestamp. If that cannot
// be established, the gap is retained with Mapped=false and an error code;
// callers must not silently move or erase it.
func AssignGapIntervals(gaps []Gap, ordered []Captured, samplesPerPacket uint32, mapTS func(ts uint32) (int64, error)) []Gap {
	bySSRC := map[uint32][]Captured{}
	for _, c := range ordered {
		bySSRC[c.Packet.SSRC] = append(bySSRC[c.Packet.SSRC], c)
	}
	for i := range gaps {
		g := &gaps[i]
		xs := bySSRC[g.SSRC]
		var prev, next *Captured
		for j := range xs {
			switch {
			case xs[j].Ext < g.StartExt:
				prev = &xs[j]
			case xs[j].Ext > g.EndExt && next == nil:
				next = &xs[j]
			}
		}
		switch {
		case prev != nil && samplesPerPacket > 0:
			startTS := prev.Packet.Timestamp + samplesPerPacket
			startNS, err := mapTS(startTS)
			if err != nil {
				g.MapError = err.Error()
				continue
			}
			g.StartTS = startTS
			g.StartNS = startNS
			if next != nil {
				endNS, err := mapTS(next.Packet.Timestamp)
				if err != nil {
					g.MapError = err.Error()
					continue
				}
				g.EndTS = next.Packet.Timestamp
				g.EndNS = endNS
			} else {
				endTS := startTS + uint32(g.Missing)*samplesPerPacket
				endNS, err := mapTS(endTS)
				if err != nil {
					g.MapError = err.Error()
					continue
				}
				g.EndTS = endTS
				g.EndNS = endNS
			}
			g.Mapped = g.EndNS > g.StartNS
			if !g.Mapped {
				g.MapError = "non-increasing mapped gap interval"
			}
		case next != nil && samplesPerPacket > 0:
			endNS, err := mapTS(next.Packet.Timestamp)
			if err != nil {
				g.MapError = err.Error()
				continue
			}
			startTS := next.Packet.Timestamp - uint32(g.Missing)*samplesPerPacket
			startNS, err := mapTS(startTS)
			if err != nil {
				g.MapError = err.Error()
				continue
			}
			g.StartTS, g.EndTS = startTS, next.Packet.Timestamp
			g.StartNS, g.EndNS, g.Mapped = startNS, endNS, startNS < endNS
		default:
			g.MapError = "insufficient adjacent RTP packets to map gap"
		}
	}
	return gaps
}
