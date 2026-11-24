package fixture

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediasync/internal/model"
)

// genEvent is one scheduled capture event before serialization.
type genEvent struct {
	trackID  uint32
	kind     model.TrackKind
	ssrc     uint32
	seqRaw   uint32
	tsRaw    uint64
	arrival  model.Nanos
	keyframe bool
	size     int
	index    int
	order    int
	repair   bool
	carries  []uint32
}

// ticksToNanos converts track-timescale ticks to the kernel time base.
func ticksToNanos(ticks float64, timescale uint32) model.Nanos {
	return model.Nanos(ticks * float64(model.NanosPerSecond) / float64(timescale))
}

// masks returns the sequence and timestamp wrap masks. Widths below the wire
// field width exercise the wrap path.
func masks(bits, tsBits uint) (uint32, uint64) {
	seqMask := ^uint32(0)
	if bits < 32 {
		seqMask = uint32(1)<<bits - 1
	}
	tsMask := ^uint64(0)
	if tsBits < 64 {
		tsMask = uint64(1)<<tsBits - 1
	}
	return seqMask, tsMask
}

// writeEvents schedules every track's samples, injects network effects and
// emits the NDJSON log.
func writeEvents(dir string, cfg CaptureConfig, plans []TrackPlan, rng *model.RNG) (string, error) {
	seqMask, tsMask := masks(cfg.WrapBits, cfg.TsWrapBits)
	var events []genEvent
	for _, p := range plans {
		for i := 0; i < p.Count; i++ {
			keyframe := p.Track.kind == model.KindVideo && i%p.Track.keyEvery == 0
			// Network delay trails the media time by a bounded random amount,
			// kept below the packet spacing so arrive-order pairing is stable.
			delay := model.Nanos(rng.Intn(6)) * model.Nanos(model.NanosPerMilli)
			media := ticksToNanos(float64(p.NominalTicks[i]), p.Track.timescale)
			events = append(events, genEvent{
				trackID: p.Track.id, kind: p.Track.kind, ssrc: p.Track.ssrc,
				seqRaw:   (p.SeqStart + uint32(i)) & seqMask,
				tsRaw:    (p.TsStart + uint64(p.Ticks[i])) & tsMask,
				arrival:  media + delay,
				keyframe: keyframe, size: 200 + int(rng.Intn(200)), index: i,
			})
		}
	}

	// Delivery order: arrival, then track, then sample index.
	byArrival := func(e []genEvent) {
		sort.SliceStable(e, func(i, j int) bool {
			if e[i].arrival != e[j].arrival {
				return e[i].arrival < e[j].arrival
			}
			if e[i].trackID != e[j].trackID {
				return e[i].trackID < e[j].trackID
			}
			if e[i].index != e[j].index {
				return e[i].index < e[j].index
			}
			return e[i].order < e[j].order
		})
	}
	byArrival(events)
	for i := range events {
		events[i].order = i
	}

	// Late deliveries: push a few early events far past the late window.
	for n := 0; n < cfg.LateEvents && len(events) > 20; n++ {
		idx := int(rng.Intn(int64(len(events) / 2)))
		events[idx].arrival += model.Nanos(cfg.DurationMS*int64(model.NanosPerMilli)/4) + model.Nanos(model.NanosPerMilli)
	}
	// Reordering: swap adjacent arrival positions inside a short window.
	for n := 0; n < cfg.ReorderEvents && len(events) > 4; n++ {
		idx := 1 + int(rng.Intn(int64(len(events)-2)))
		events[idx].arrival, events[idx+1].arrival = events[idx+1].arrival, events[idx].arrival
	}
	// Duplicates: copy a few already-scheduled events.
	dups := make([]genEvent, 0, cfg.Duplicates)
	for n := 0; n < cfg.Duplicates && len(events) > 4; n++ {
		idx := int(rng.Intn(int64(len(events))))
		dup := events[idx]
		dup.arrival += model.Nanos(model.NanosPerMilli)
		dup.order = len(events) + n
		dups = append(dups, dup)
	}
	events = append(events, dups...)
	byArrival(events)

	// Loss bursts: each burst drops a run of consecutive packets from a
	// single track, so sequence gaps stay per-track. Every second burst is
	// recovered by a redundancy record that names the lost sequence numbers
	// without carrying media content.
	dropped := map[[2]uint32]bool{}
	perTrack := map[uint32][]int{}
	for i := range events {
		if events[i].repair {
			continue
		}
		perTrack[events[i].trackID] = append(perTrack[events[i].trackID], i)
	}
	trackIDs := make([]uint32, 0, len(perTrack))
	for id := range perTrack {
		trackIDs = append(trackIDs, id)
	}
	sort.Slice(trackIDs, func(i, j int) bool { return trackIDs[i] < trackIDs[j] })
	repairs := make([]genEvent, 0, cfg.LossBursts)
	for n := 0; n < cfg.LossBursts && len(trackIDs) > 0; n++ {
		id := trackIDs[n%len(trackIDs)]
		positions := perTrack[id]
		if len(positions) < 30 {
			continue
		}
		start := 10 + int(rng.Intn(int64(len(positions)-20)))
		length := 2 + int(rng.Intn(4))
		if start+length > len(positions) {
			continue
		}
		var last genEvent
		carries := make([]uint32, 0, length)
		for k := 0; k < length; k++ {
			ev := events[positions[start+k]]
			dropped[[2]uint32{ev.trackID, uint32(ev.index)}] = true
			last = ev
			carries = append(carries, ev.seqRaw)
		}
		if n%2 == 0 {
			repairs = append(repairs, genEvent{
				trackID: id, kind: last.kind, seqRaw: last.seqRaw,
				arrival: last.arrival + model.Nanos(model.NanosPerMilli),
				repair:  true, carries: carries,
			})
		}
	}
	events = append(events, repairs...)
	byArrival(events)

	path := filepath.Join(dir, "capture.ndjson")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<16)
	enc := json.NewEncoder(w)
	write := func(ev genEvent) error {
		out := model.PacketEvent{
			TrackID: ev.trackID, Kind: ev.kind, SSRC: ev.ssrc,
			Seq: ev.seqRaw, Timestamp: ev.tsRaw, Arrival: int64(ev.arrival),
			Keyframe: ev.keyframe, Size: ev.size,
		}
		if ev.repair {
			out.Type = model.EventRepair
			out.Carries = ev.carries
		}
		return enc.Encode(out)
	}
	for _, ev := range events {
		if !ev.repair && dropped[[2]uint32{ev.trackID, uint32(ev.index)}] {
			continue
		}
		if err := write(ev); err != nil {
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return path, nil
}
