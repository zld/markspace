// Package verify checks the remuxed output against the input independently of
// the remux writer. For every track it compares payload hashes, packet counts
// and time ranges; a mismatch is reported with the specific track and the
// first differing packet, never as a generic failure.
package verify

import (
	"fmt"
	"sort"

	"archive/internal/demux"
	"archive/internal/fault"
	"archive/internal/model"
	"archive/internal/probe"
)

// TrackCheck is one track's comparison.
type TrackCheck struct {
	Logical       string `json:"logical"`
	InputKey      string `json:"input_key"`
	OutputKey     string `json:"output_key"`
	InputCount    int    `json:"input_packets"`
	OutputCount   int    `json:"output_packets"`
	HashesMatch   bool   `json:"hashes_match"`
	CountMatch    bool   `json:"count_match"`
	RangeMatch    bool   `json:"range_match"`
	FirstDiff     int    `json:"first_diff,omitempty"`
	InputStartNS  int64  `json:"input_start_ns"`
	InputEndNS    int64  `json:"input_end_ns"`
	OutputStartNS int64  `json:"output_start_ns"`
	OutputEndNS   int64  `json:"output_end_ns"`
}

// Report is the verification result.
type Report struct {
	OK      bool          `json:"ok"`
	Tracks  []*TrackCheck `json:"tracks"`
	Faults  fault.List    `json:"-"`
	Summary string        `json:"summary"`
}

// OutputBytes re-probes and re-demuxes the produced output. The output is
// treated as a black box: verification does not reuse any state from the
// writer except the input side's packet list.
func Check(in *demux.Result, outData []byte) (*Report, error) {
	src := probe.Source{Name: "remux-out", Data: outData}
	f, err := probe.Probe(src, model.ContainerMP4)
	if err != nil {
		return nil, &fault.Error{Code: fault.VerifyMismatch, Detail: "verify: output is unreadable: " + err.Error()}
	}
	out, err := demux.Demux(src, f)
	if err != nil {
		return nil, err
	}
	rep := &Report{OK: true}
	// Index the output by logical name; logical names are unique in a single
	// output container.
	outByLogical := map[string]*demux.TrackData{}
	for _, td := range out.Tracks {
		outByLogical[td.Logical] = td
	}
	inTracks := append([]*demux.TrackData(nil), in.Tracks...)
	sort.Slice(inTracks, func(i, j int) bool { return inTracks[i].Logical < inTracks[j].Logical })
	for _, it := range inTracks {
		if it.Kind == model.KindData && len(it.Packets) == 0 {
			continue
		}
		ot := outByLogical[it.Logical]
		tc := &TrackCheck{Logical: it.Logical, InputKey: it.Key, InputCount: len(it.Packets)}
		if ot != nil {
			tc.OutputKey = ot.Key
			tc.OutputCount = len(ot.Packets)
		}
		tc.CountMatch = tc.InputCount == tc.OutputCount
		if it.Timebase.Timescale > 0 && len(it.Packets) > 0 {
			r := it.Range()
			tc.InputStartNS = it.Timebase.TickToNanos(r.StartTick)
			tc.InputEndNS = it.Timebase.TickToNanos(r.StartTick + r.Duration)
		}
		if ot != nil && ot.Timebase.Timescale > 0 && len(ot.Packets) > 0 {
			r := ot.Range()
			tc.OutputStartNS = ot.Timebase.TickToNanos(r.StartTick)
			tc.OutputEndNS = ot.Timebase.TickToNanos(r.StartTick + r.Duration)
		}
		tc.RangeMatch = tc.InputStartNS == tc.OutputStartNS && tc.InputEndNS == tc.OutputEndNS
		tc.HashesMatch = true
		n := min(len(it.Packets), len(otPackets(ot)))
		for i := 0; i < n; i++ {
			if it.Packets[i].SHA256 != ot.Packets[i].SHA256 {
				tc.HashesMatch = false
				tc.FirstDiff = it.Packets[i].Seq
				rep.Faults.Add(&fault.Error{
					Code: fault.VerifyMismatch, Track: it.Key, TrackID: it.Packets[i].TrackID,
					StartNS:  it.Timebase.TickToNanos(it.Packets[i].StartTick),
					EndNS:    it.Timebase.TickToNanos(it.Packets[i].StartTick + it.Packets[i].Duration),
					HasRange: true, Recoverable: false,
					Detail: fmt.Sprintf("verify: %s packet %d payload hash %s != %s",
						it.Logical, it.Packets[i].Seq, it.Packets[i].SHA256, ot.Packets[i].SHA256),
				})
				break
			}
		}
		if !tc.HashesMatch || !tc.CountMatch || !tc.RangeMatch {
			rep.OK = false
			if !rep.Faults.HasTrack(it.Key) {
				rep.Faults.Add(&fault.Error{
					Code: fault.VerifyMismatch, Track: it.Key, TrackID: 0,
					Recoverable: false,
					Detail: fmt.Sprintf("verify: %s count %d/%d range %d-%d/%d-%d hashes=%t",
						it.Logical, tc.InputCount, tc.OutputCount, tc.InputStartNS, tc.InputEndNS,
						tc.OutputStartNS, tc.OutputEndNS, tc.HashesMatch),
				})
			}
		}
		rep.Tracks = append(rep.Tracks, tc)
	}
	if rep.OK {
		rep.Summary = fmt.Sprintf("verify ok tracks=%d", len(rep.Tracks))
	} else {
		rep.Summary = fmt.Sprintf("verify FAILED tracks=%d", len(rep.Tracks))
	}
	rep.Faults = rep.Faults.Dedup()
	return rep, nil
}

func otPackets(t *demux.TrackData) []*demux.Packet {
	if t == nil {
		return nil
	}
	return t.Packets
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
