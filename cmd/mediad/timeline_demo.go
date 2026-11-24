package main

import (
	"fmt"

	"mediad/internal/model"
	"mediad/internal/timeline"
)

// buildTimeline feeds a fixed event set in a deliberately scrambled arrival
// order. In-window late events trigger recompute; one beyond-window event is
// rejected with a structured LATE_REJECTED error.
func buildTimeline(lateWindowMS int64, r *rng) (timeline.Snapshot, []string) {
	windowNS := lateWindowMS * 1_000_000
	eng, err := timeline.NewEngine(timeline.EngineConfig{LateWindowNS: windowNS})
	if err != nil {
		panic(err) // validated config; unreachable for positive CLI input
	}
	codes := map[string]bool{}

	// Event times in ms on the shared nanosecond clock.
	base := []timeline.Event{
		{ID: "ev-0", TimeNS: 0, Track: 1, Payload: []byte("a0")},
		{ID: "ev-10", TimeNS: 10_000_000, Track: 1, Payload: []byte("a10")},
		{ID: "ev-20", TimeNS: 20_000_000, Track: 2, Payload: []byte("v20")},
		{ID: "ev-30", TimeNS: 30_000_000, Track: 2, Payload: []byte("v30")},
		{ID: "ev-40", TimeNS: 40_000_000, Track: 1, Payload: []byte("a40")},
	}
	// Deterministic scrambled arrival order driven by the seed.
	order := perm(len(base), r)
	for _, idx := range order {
		must(eng.Push(base[idx]))
	}

	// Late event inside the window (watermark 40ms, event at 25ms).
	inWindow := timeline.Event{ID: "late-in", TimeNS: 25_000_000, Track: 2,
		Payload: []byte(fmt.Sprintf("seed-%d", r.seed))}
	must(eng.Push(inWindow))

	// Beyond-window event (lag well over the configured window) -> rejected.
	tooLate := timeline.Event{ID: "late-out", TimeNS: -windowNS * 2, Track: 3, Payload: []byte("old")}
	if err := eng.Push(tooLate); err != nil {
		if me := model.AsMediaError(err); me != nil {
			codes[string(me.Code)] = true
		}
	}
	return eng.Snapshot(), sortedKeys(codes)
}

// perm returns a deterministic Fisher-Yates permutation of [0,n).
func perm(n int, r *rng) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := r.intn(i + 1)
		p[i], p[j] = p[j], p[i]
	}
	return p
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
