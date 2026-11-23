package pipeline

import (
	"sort"

	"mediaqc/internal/model"
)

func sequences(evs []model.Event) []uint64 {
	out := make([]uint64, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Sequence)
	}
	return out
}

func recoveredSeq(evs []model.Event) []uint64 {
	var out []uint64
	for _, ev := range evs {
		if ev.Recovered {
			out = append(out, ev.Sequence)
		}
	}
	return out
}

func sortEvents(evs []model.Event) {
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Sequence < evs[j].Sequence })
}

func appendGap(gaps []model.Gap, g model.Gap) []model.Gap {
	for _, existing := range gaps {
		if existing.FirstSeq == g.FirstSeq && existing.LastSeq == g.LastSeq {
			return gaps
		}
	}
	return append(gaps, g)
}

func putUint64(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}
