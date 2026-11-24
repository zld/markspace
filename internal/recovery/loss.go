// Package recovery turns validated records and jitter-buffer output into a
// canonical timeline with explicit missing/concealment markers. It never
// invents image content or claims to reconstruct lost audio samples.
package recovery

import (
	"fmt"
	"sort"

	"mediarc/internal/buffer"
	"mediarc/internal/ingest"
	"mediarc/internal/model"
)

// FrameState is the recovery state of one video frame slot.
type FrameState string

const (
	FramePresent FrameState = "present"
	FrameMissing FrameState = "missing"
)

// RestoredFrame is a canonical timeline slot. Winner is set only when a
// checksum-valid record won timestamp deduplication.
type RestoredFrame struct {
	Index       int               `json:"index"`
	PTS         uint64            `json:"pts"`
	Duration    uint32            `json:"duration"`
	State       FrameState        `json:"state"`
	Reason      model.Kind        `json:"reason,omitempty"`
	Winner      *ingest.FrameMeta `json:"winner,omitempty"`
	Candidates  int               `json:"candidates"`
	DuplicateOf int               `json:"duplicate_of,omitempty"`
	Marker      []byte            `json:"-"`
}

// FrameReport contains the sorted canonical video timeline.
type FrameReport struct {
	Frames          []RestoredFrame `json:"frames"`
	FrameTotal      int             `json:"frame_total"`
	Present         int             `json:"present"`
	Missing         int             `json:"missing_frames"`
	DuplicateGroups int             `json:"duplicate_groups"`
}

// RestoreFrames deduplicates equal-PTS records using checksum validity first
// and smaller original offset second, then lays records on the declared grid.
func RestoreFrames(metas []ingest.FrameMeta, expectedCount int, faults *model.Collector) *FrameReport {
	if expectedCount <= 0 {
		expectedCount = len(metas)
	}
	groups := make(map[uint64][]ingest.FrameMeta)
	var duration uint32 = uint32(model.VideoFrameDelta)
	for _, m := range metas {
		if m.Duration != 0 {
			duration = m.Duration
		}
		groups[m.PTS] = append(groups[m.PTS], m)
	}

	frames := make([]RestoredFrame, expectedCount)
	for i := range frames {
		pts := uint64(i) * uint64(duration)
		frames[i] = RestoredFrame{Index: i, PTS: pts, Duration: duration, State: FrameMissing,
			Reason: model.FaultRTPLoss, Marker: MissingFrameMarker(i)}
	}

	report := &FrameReport{Frames: frames, FrameTotal: expectedCount}
	ptsList := make([]uint64, 0, len(groups))
	for pts := range groups {
		ptsList = append(ptsList, pts)
	}
	sort.Slice(ptsList, func(i, j int) bool { return ptsList[i] < ptsList[j] })

	for _, pts := range ptsList {
		candidates := groups[pts]
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Valid != candidates[j].Valid {
				return candidates[i].Valid // valid (true) first
			}
			return candidates[i].Offset < candidates[j].Offset
		})
		winner := candidates[0]
		idx := int(winner.RecordIndex)
		if winner.TimestampOK && duration > 0 {
			idx = int(pts / uint64(duration))
		}
		if idx < 0 || idx >= expectedCount {
			continue
		}
		rf := &report.Frames[idx]
		rf.PTS = pts
		rf.Duration = duration
		rf.Candidates = len(candidates)
		if len(candidates) > 1 {
			report.DuplicateGroups++
			for _, loser := range candidates[1:] {
				faults.Add(model.Fault{Kind: model.FaultDuplicateFrame, MaterialID: loser.MaterialID,
					RecordIndex: int64(loser.RecordIndex), Offset: loser.Offset,
					RecordStart: loser.Offset, Message: "duplicate frame timestamp; loser discarded",
					Recoverable: true, Recovered: winner.Valid})
			}
		}
		if !winner.Valid {
			rf.State = FrameMissing
			rf.Winner = nil
			rf.Reason = dominantReason(winner)
			rf.Marker = MissingFrameMarker(idx)
			report.Missing++
			continue
		}
		winnerCopy := winner
		rf.Winner = &winnerCopy
		rf.State = FramePresent
		rf.Reason = ""
		rf.Marker = nil
		report.Present++
	}
	return report
}

func dominantReason(m ingest.FrameMeta) model.Kind {
	if !m.LengthOK {
		return model.FaultTruncated
	}
	if !m.TimestampOK {
		return model.FaultBadTimestamp
	}
	if !m.ChecksumOK {
		return model.FaultBadSHA
	}
	return "invalid_frame"
}

// MissingFrameMarker returns the explicit non-image marker stored for a gap.
func MissingFrameMarker(index int) []byte {
	return []byte(fmt.Sprintf("MISSING-FRAME:%06d", index))
}

// AudioBlock is one ordered RTP-derived audio block or an explicit gap marker.
type AudioBlock struct {
	Sequence  int64  `json:"sequence"`
	Timestamp uint32 `json:"timestamp"`
	Payload   []byte `json:"-"`
	Concealed bool   `json:"concealed"`
	Missing   bool   `json:"missing"`
}

// AudioReport contains ordered blocks and deterministic concealment counts.
type AudioReport struct {
	Blocks           []AudioBlock `json:"-"`
	PacketCount      int          `json:"packet_count"`
	GapCount         int          `json:"gap_count"`
	ConcealedPackets int          `json:"concealed_packets"`
	ConcealedSamples int          `json:"concealed_samples"`
}

// FillAudioGaps inserts explicit silence/marker blocks for every closed-window
// sequence gap. Present payloads are passed through untouched.
func FillAudioGaps(items []buffer.Item, gaps []buffer.Gap, samplesPerPacket, channels uint32) *AudioReport {
	if samplesPerPacket == 0 {
		samplesPerPacket = 480
	}
	if channels == 0 {
		channels = 2
	}
	gapSet := make(map[uint64]int)
	for _, g := range gaps {
		for seq := g.FromExt; seq <= g.ToExt; seq++ {
			gapSet[seq] = int(samplesPerPacket)
		}
	}
	if len(items) == 0 {
		return &AudioReport{}
	}
	first := items[0].ExtSequence
	last := items[len(items)-1].ExtSequence
	itemByExt := make(map[uint64]buffer.Item, len(items))
	for _, it := range items {
		itemByExt[it.ExtSequence] = it
	}
	rep := &AudioReport{}
	for seq := first; seq <= last; seq++ {
		if it, ok := itemByExt[seq]; ok {
			rep.Blocks = append(rep.Blocks, AudioBlock{
				Sequence: int64(seq), Timestamp: it.Stored.Packet.Timestamp,
				Payload: it.Stored.Packet.Payload,
			})
			rep.PacketCount++
			continue
		}
		samples := gapSet[seq]
		// Signed-16 PCM zero is deterministic silence; Missing prevents it
		// from being interpreted as recovered original audio.
		payload := make([]byte, samples*int(channels)*2)
		ts := uint32(seq-first) * samplesPerPacket
		rep.Blocks = append(rep.Blocks, AudioBlock{Sequence: int64(seq), Timestamp: ts,
			Payload: payload, Concealed: true, Missing: true})
		rep.GapCount++
		rep.ConcealedPackets++
		rep.ConcealedSamples += samples * int(channels)
	}
	return rep
}
