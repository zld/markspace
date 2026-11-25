// Package fragment builds byte/time mapped fragments and validates boundary,
// cross-track coverage and RTP continuity through one error model.
package fragment

import (
	"fmt"
	"sort"

	"mediacheck/internal/ebml"
	"mediacheck/internal/report"
	"mediacheck/internal/rtp"
	"mediacheck/internal/timeline"
	"mediacheck/internal/tracks"
)

// Fragment is one Cluster-level distribution unit.
type Fragment struct {
	ID            string
	Index         int
	ClusterOffset int
	EndOffset     int
	StartNS       int64
	EndNS         int64
	ByteLength    int64
	Tracks        []int
}

// Result contains fragments and all fixed-schema output records.
type Result struct {
	Fragments []Fragment
	Records   []report.Record
}

// Check maps every EBML block to a fragment and validates fragment and RTP
// constraints. Structural timestamp conversion failures are returned; policy
// conflicts are accumulated as records.
func Check(doc *ebml.Document, graph *tracks.Graph, streams []rtp.Stream) (*Result, *report.Error) {
	clock, err := timeline.NewMatroskaClock(doc.TimecodeScale, doc.Limits.MaxFragmentSpan)
	if err != nil {
		return nil, err
	}
	res := &Result{}

	for _, cluster := range doc.Clusters {
		frag := Fragment{
			ID:            fmt.Sprintf("F%04d", cluster.Index+1),
			Index:         cluster.Index,
			ClusterOffset: cluster.Element.Offset,
			EndOffset:     cluster.Element.End,
			ByteLength:    int64(cluster.Element.End - cluster.Element.Offset),
		}
		clusterNS, cerr := clock.BlockNS(cluster.Timecode, 0, cluster.Element.Offset, cluster.TimecodeOffset)
		if cerr != nil {
			return nil, cerr
		}
		frag.StartNS = clusterNS
		frag.EndNS = clusterNS

		prevTrackNS := map[uint64]int64{}
		trackBlocks := map[uint64][]*ebml.Block{}
		prevRawOffset := -1
		trackSeen := map[int]bool{}

		for _, b := range cluster.Blocks {
			ns, nerr := clock.BlockNS(cluster.Timecode, b.RelativeTicks, b.Element.Offset, b.Element.End)
			if nerr != nil {
				if e, ok := report.AsError(nerr); ok {
					e.Record.ElementID = ebml.HexID(b.Element.ID)
					e.Record.Track = int(b.Track)
				}
				return nil, nerr
			}
			timeDelta := int64(0)
			if prev, ok := prevTrackNS[b.Track]; ok {
				timeDelta = ns - prev
			}
			byteDelta := int64(0)
			if prevRawOffset >= 0 {
				byteDelta = int64(b.Element.Offset - prevRawOffset)
			}
			prevTrackNS[b.Track] = ns
			prevRawOffset = b.Element.Offset
			trackBlocks[b.Track] = append(trackBlocks[b.Track], b)
			trackSeen[int(b.Track)] = true
			if ns > frag.EndNS {
				frag.EndNS = ns
			}

			res.Records = append(res.Records, report.Record{
				Type:          "block",
				Track:         int(b.Track),
				InputOffset:   b.Element.Offset,
				FailureOffset: -1,
				TimeDeltaNS:   timeDelta,
				ByteDelta:     byteDelta,
				ErrorCode:     report.OK,
				Fragment:      frag.ID,
				BlockIndex:    b.Index,
				TimeNS:        ns,
				TimeStartNS:   ns,
				Message:       fmt.Sprintf("raw_order=%d track=%d relative_ticks=%d", b.Index, b.Track, b.RelativeTicks),
			})

			if ns < clusterNS {
				res.Records = append(res.Records, issue(int(b.Track), b.Element.Offset, b.Element.End,
					report.FragmentTimeOrder, "block_before_cluster_timecode",
					"block timestamp %d is before Cluster timecode %d", ns, clusterNS))
			}
		}

		frag.Tracks = sortedTracks(trackSeen)
		span := frag.EndNS - frag.StartNS
		if span < 0 {
			res.Records = append(res.Records, issue(0, frag.ClusterOffset, frag.EndOffset,
				report.FragmentTimeOrder, "negative_fragment_span",
				"fragment %s has negative time span %d", frag.ID, span))
		}
		if span > doc.Limits.MaxFragmentSpan {
			res.Records = append(res.Records, issue(0, frag.ClusterOffset, frag.EndOffset,
				report.FragmentLimit, "fragment_span_too_large",
				"fragment %s span %dns exceeds limit %dns", frag.ID, span, doc.Limits.MaxFragmentSpan))
		}
		if graph != nil {
			for _, trackNumber := range sortedGraphTracks(graph) {
				if !trackSeen[int(trackNumber)] {
					res.Records = append(res.Records, issue(int(trackNumber), frag.ClusterOffset, frag.EndOffset,
						report.FragmentCoverage, "missing_track_in_fragment",
						"fragment %s has no block for track %d", frag.ID, trackNumber))
				}
			}
		}

		res.Fragments = append(res.Fragments, frag)
		res.Records = append(res.Records, report.Record{
			Type:          "fragment",
			InputOffset:   frag.ClusterOffset,
			FailureOffset: -1,
			TimeDeltaNS:   span,
			ByteDelta:     frag.ByteLength,
			ErrorCode:     report.OK,
			Fragment:      frag.ID,
			TimeStartNS:   frag.StartNS,
			TimeEndNS:     frag.EndNS,
			Message:       fmt.Sprintf("tracks=%v blocks=%d", frag.Tracks, len(cluster.Blocks)),
		})

		attachRTP(res, streams, frag, trackBlocks)
	}
	return res, nil
}

func attachRTP(res *Result, streams []rtp.Stream, frag Fragment, blocks map[uint64][]*ebml.Block) {
	for _, stream := range streams {
		if stream.FragmentIndex != frag.Index {
			continue
		}
		allowed := timeline.TimeRange{StartNS: 0, EndNS: frag.EndNS - frag.StartNS}
		rtpResult := rtp.ValidateStream(stream, allowed, 0)
		for _, is := range rtpResult.Issues {
			is.Fragment = frag.ID
			if is.InputOffset == 0 {
				is.InputOffset = frag.ClusterOffset
			}
			res.Records = append(res.Records, is)
		}
		if len(stream.Packets) == 0 {
			continue
		}
		firstSeq := stream.Packets[0].SequenceNumber
		for _, missing := range rtpResult.Missing {
			offset := -1
			trackBlocks := blocks[uint64(missing.Track)]
			index := int(missing.Sequence - firstSeq)
			if index >= 0 && index < len(trackBlocks) {
				offset = trackBlocks[index].Element.Offset
			}
			res.Records = append(res.Records, report.Record{
				Type:          "recovery",
				Track:         missing.Track,
				InputOffset:   offset,
				FailureOffset: offset,
				TimeDeltaNS:   missing.TimeRange.Duration(),
				ByteDelta:     missing.ByteDelta,
				ErrorCode:     report.RTPGapRecovered,
				Category:      "missing_rtp_sequence",
				Severity:      report.SeverityRecovered,
				Fragment:      frag.ID,
				Sequence:      int(missing.Sequence),
				MissingCount:  1,
				TimeStartNS:   missing.TimeRange.StartNS,
				TimeEndNS:     missing.TimeRange.EndNS,
				Message: fmt.Sprintf("recovered missing RTP seq=%d between %d and %d; time range [%d,%d) ns",
					missing.Sequence, missing.PrevSequence, missing.NextSequence,
					missing.TimeRange.StartNS, missing.TimeRange.EndNS),
			})
		}
	}
}

func sortedTracks(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func sortedGraphTracks(g *tracks.Graph) []uint64 {
	out := make([]uint64, 0, len(g.Tracks))
	for n := range g.Tracks {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func issue(track, inputOffset, failureOffset int, code, category, format string, args ...any) report.Record {
	return report.Record{
		Type:          "issue",
		Track:         track,
		InputOffset:   inputOffset,
		FailureOffset: failureOffset,
		ErrorCode:     code,
		Category:      category,
		Severity:      report.SeverityError,
		Message:       fmt.Sprintf(format, args...),
	}
}
