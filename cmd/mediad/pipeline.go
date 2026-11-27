package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"mediad/internal/clock"
	"mediad/internal/ebml"
	"mediad/internal/fixture"
	"mediad/internal/index"
	"mediad/internal/job"
	"mediad/internal/merge"
)

// mapAndIndex parses a Matroska file and maps all events to the unified
// nanosecond timeline, returning timed events and the keyframe index.
func mapAndIndex(data []byte) ([]index.TimedEvent, *index.Index, error) {
	nodes, err := ebml.NewParser(data).Parse()
	if err != nil {
		return nil, nil, err
	}
	var seg *ebml.Node
	for _, n := range nodes {
		if n.ID == ebml.IDSegment {
			seg = n
		}
	}
	if seg == nil {
		return nil, nil, clock.New(clock.ErrBounds, "no segment found")
	}
	scale, tracks, err := ebml.SegmentInfo(seg)
	if err != nil {
		return nil, nil, err
	}
	tl := clock.NewTimeline()
	for _, tr := range tracks {
		m, err := clock.MatroskaMapping(scale, 1, 1, 0)
		if err != nil {
			return nil, nil, err
		}
		if err := tl.Bind(tr, m); err != nil {
			return nil, nil, err
		}
	}
	events, err := ebml.Events(seg)
	if err != nil {
		return nil, nil, err
	}
	var timed []index.TimedEvent
	var maxNs int64
	for _, ev := range events {
		ns, err := tl.Map(ev.Track, ev.Tick)
		if err != nil {
			return nil, nil, err
		}
		timed = append(timed, index.TimedEvent{Event: ev, TimeNs: ns})
		if ns > maxNs {
			maxNs = ns
		}
	}
	duration := maxNs + 40_000_000 // last block duration
	return timed, index.Build(timed, int64(len(data)), duration), nil
}

// readRTPEvents parses length-prefixed RTP packets and maps their
// timestamps (90 kHz) onto the unified timeline.
func readRTPEvents(path string) ([]int64, error) {
	data, err := readCapped(path)
	if err != nil {
		return nil, err
	}
	m, err := clock.RTPMapping(90000, 0)
	if err != nil {
		return nil, err
	}
	var out []int64
	for len(data) >= 2 {
		n := int(binary.BigEndian.Uint16(data))
		data = data[2:]
		if n == 0 || n > len(data) {
			return nil, clock.New(clock.ErrCorruptBlock, "RTP record length %d out of range", n)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data[:n]); err != nil {
			return nil, clock.New(clock.ErrCorruptBlock, "RTP unmarshal: %v", err)
		}
		ns, err := m.Map(int64(pkt.Timestamp))
		if err != nil {
			return nil, err
		}
		out = append(out, ns)
		data = data[n:]
	}
	return out, nil
}

// checkMP4Timescale confirms the MP4 mvhd timescale (units/second) matches
// the Matroska timebase: 1000 units/s == 1ms per unit.
func checkMP4Timescale(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	boxes, err := mp4.ExtractBoxWithPayload(f, nil,
		mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()})
	if err != nil {
		return false, err
	}
	if len(boxes) != 1 {
		return false, nil
	}
	mvhd, ok := boxes[0].Payload.(*mp4.Mvhd)
	if !ok {
		return false, nil
	}
	return mvhd.Timescale == 1_000_000_000/fixture.TimecodeScaleNs, nil
}

// recoveryPath runs the job pipeline with a simulated crash, then resumes
// and reruns to prove checkpoint recovery and hash determinism.
func recoveryPath(work string, data []byte) (resumed, rerunMatch bool, err error) {
	// The recovery demonstration always starts from a clean store so verify
	// is idempotent when re-run in the same work directory.
	if err := os.RemoveAll(filepath.Join(work, "job")); err != nil {
		return false, false, err
	}
	store, err := job.NewStore(filepath.Join(work, "job"))
	if err != nil {
		return false, false, err
	}
	phases := []job.Phase{job.PhaseParse, job.PhaseMap, job.PhaseIndex, job.PhaseMerge}
	fns := func(crash bool) map[job.Phase]job.PhaseFunc {
		return map[job.Phase]job.PhaseFunc{
			job.PhaseParse: func(prev []byte) ([]byte, error) { return data, nil },
			job.PhaseMap: func(prev []byte) ([]byte, error) {
				timed, _, err := mapAndIndex(prev)
				if err != nil {
					return nil, err
				}
				return json.Marshal(timed)
			},
			job.PhaseIndex: func(prev []byte) ([]byte, error) {
				var timed []index.TimedEvent
				if err := json.Unmarshal(prev, &timed); err != nil {
					return nil, err
				}
				var maxNs int64
				for _, ev := range timed {
					if ev.TimeNs > maxNs {
						maxNs = ev.TimeNs
					}
				}
				ix := index.Build(timed, int64(len(data)), maxNs+40_000_000)
				return json.Marshal(ix.Keys)
			},
			job.PhaseMerge: func(prev []byte) ([]byte, error) {
				if crash {
					return nil, fmt.Errorf("simulated interruption")
				}
				var keys []index.Keyframe
				if err := json.Unmarshal(prev, &keys); err != nil {
					return nil, err
				}
				var segs []merge.Segment
				for i, k := range keys {
					end := k.TimeNs + 80_000_000
					srcEnd := int64(len(data))
					if i+1 < len(keys) {
						srcEnd = keys[i+1].Offset
					}
					segs = append(segs, merge.Segment{
						TaskKey:   fmt.Sprintf("seg-%03d", i),
						StartNs:   k.TimeNs,
						EndNs:     end,
						SrcOffset: k.Offset,
						SrcLength: srcEnd - k.Offset,
						SHA256:    merge.HashBytes(data[k.Offset:srcEnd]),
					})
				}
				m, err := merge.Merge(segs)
				if err != nil {
					return nil, err
				}
				return m.Bytes()
			},
		}
	}
	// First run crashes at the merge phase.
	if _, err := job.Run(store, "acceptance", phases, fns(true)); err == nil {
		return false, false, fmt.Errorf("expected simulated crash")
	}
	// Second run resumes from the last complete checkpoint.
	res2, err := job.Run(store, "acceptance", phases, fns(false))
	if err != nil {
		return false, false, err
	}
	// Third run is fully checkpoint-served; digest must be identical.
	res3, err := job.Run(store, "acceptance", phases, fns(false))
	if err != nil {
		return false, false, err
	}
	return res2.ResumedFromCheckpoint, res2.Digest == res3.Digest, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
